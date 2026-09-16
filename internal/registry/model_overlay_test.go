package registry

import (
	"os"
	"path/filepath"
	"testing"
)

const overlayTestCodexModelID = "gpt-daybreak-blue-latest"
const overlayTestMetaModelID = "meta-local-test"

const overlayTestCodexJSON = `{
  "codex": [
    {
      "id": "gpt-daybreak-blue-latest",
      "object": "model",
      "created": 1770912000,
      "owned_by": "openai",
      "type": "openai",
      "display_name": "Daybreak Blue",
      "version": "gpt-daybreak-blue",
      "description": "Codex preview model missing from the upstream catalog.",
      "context_length": 272000,
      "max_completion_tokens": 128000,
      "supported_parameters": ["tools"],
      "thinking": {"levels": ["low", "medium", "high", "xhigh"]},
      "supportedInputModalities": ["text", "image"],
      "supportedOutputModalities": ["text"]
    }
  ]
}`

const overlayTestMetaJSON = `{"meta": [{"id": "meta-local-test", "object": "model", "owned_by": "meta", "type": "meta", "display_name": "Local Meta Test"}]}`

// writeOverlayFile writes content to a temporary overlay file and returns its path.
func writeOverlayFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.local.json")
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write overlay file: %v", errWrite)
	}
	return path
}

// restoreModelCatalog clears the overlay and reloads the embedded catalog so a test
// cannot leak state into the rest of the package.
func restoreModelCatalog(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ApplyModelOverlayFile("")
		if errLoad := loadModelsFromBytes(embeddedModelsJSON, "test-restore"); errLoad != nil {
			t.Fatalf("restore embedded catalog: %v", errLoad)
		}
	})
}

func findModelByID(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func TestApplyModelOverlayFileAddsCodexModel(t *testing.T) {
	restoreModelCatalog(t)

	if found := findModelByID(GetCodexProModels(), overlayTestCodexModelID); found != nil {
		t.Fatalf("%s is already present in the embedded catalog", overlayTestCodexModelID)
	}

	changed := ApplyModelOverlayFile(writeOverlayFile(t, overlayTestCodexJSON))
	if len(changed) != 1 || changed[0] != "codex" {
		t.Fatalf("changed providers = %v, want [codex]", changed)
	}

	accessors := map[string]func() []*ModelInfo{
		"codex-free":     GetCodexFreeModels,
		"codex-team":     GetCodexTeamModels,
		"codex-plus":     GetCodexPlusModels,
		"codex-pro":      GetCodexProModels,
		"codex(channel)": func() []*ModelInfo { return GetStaticModelDefinitionsByChannel("codex") },
	}
	for name, accessor := range accessors {
		model := findModelByID(accessor(), overlayTestCodexModelID)
		if model == nil {
			t.Fatalf("%s does not list %s", name, overlayTestCodexModelID)
		}
		if model.DisplayName != "Daybreak Blue" {
			t.Fatalf("%s display name = %q, want %q", name, model.DisplayName, "Daybreak Blue")
		}
		if model.Type != "openai" {
			t.Fatalf("%s type = %q, want %q", name, model.Type, "openai")
		}
	}

	if info := LookupStaticModelInfo(overlayTestCodexModelID); info == nil {
		t.Fatalf("LookupStaticModelInfo(%s) = nil, want the overlay definition", overlayTestCodexModelID)
	}

	// A remote refresh replaces the base catalog; the overlay must survive it.
	if errLoad := loadModelsFromBytes(embeddedModelsJSON, "test-refresh"); errLoad != nil {
		t.Fatalf("reload embedded catalog: %v", errLoad)
	}
	if findModelByID(GetCodexProModels(), overlayTestCodexModelID) == nil {
		t.Fatalf("%s was dropped by a catalog refresh", overlayTestCodexModelID)
	}
}

func TestApplyModelOverlayFileReplacesExistingModel(t *testing.T) {
	restoreModelCatalog(t)

	const replacedID = "gpt-5.5"
	before := GetCodexProModels()
	original := findModelByID(before, replacedID)
	if original == nil {
		t.Fatalf("embedded codex-pro catalog does not contain %s", replacedID)
	}
	if original.DisplayName == "Locally Pinned 5.5" {
		t.Fatalf("embedded display name already matches the overlay value")
	}

	overlay := `{"codex-pro": [{"id": "gpt-5.5", "object": "model", "created": 1, "owned_by": "openai", "type": "openai", "display_name": "Locally Pinned 5.5", "context_length": 400000}]}`
	ApplyModelOverlayFile(writeOverlayFile(t, overlay))

	after := GetCodexProModels()
	if len(after) != len(before) {
		t.Fatalf("codex-pro model count = %d, want %d (replacement must not append)", len(after), len(before))
	}
	replaced := findModelByID(after, replacedID)
	if replaced == nil {
		t.Fatalf("codex-pro no longer contains %s", replacedID)
	}
	if replaced.DisplayName != "Locally Pinned 5.5" || replaced.ContextLength != 400000 {
		t.Fatalf("replaced model = %+v, want the overlay definition", replaced)
	}

	// Other Codex tiers must keep the upstream definition.
	if plus := findModelByID(GetCodexPlusModels(), replacedID); plus == nil || plus.DisplayName == "Locally Pinned 5.5" {
		t.Fatalf("codex-plus %s = %+v, want the upstream definition", replacedID, plus)
	}
}

func TestApplyModelOverlayFileAddsMetaModel(t *testing.T) {
	restoreModelCatalog(t)

	if found := findModelByID(GetMetaModels(), overlayTestMetaModelID); found != nil {
		t.Fatalf("%s is already present in the embedded catalog", overlayTestMetaModelID)
	}

	changed := ApplyModelOverlayFile(writeOverlayFile(t, overlayTestMetaJSON))
	if len(changed) != 1 || changed[0] != "meta" {
		t.Fatalf("changed providers = %v, want [meta]", changed)
	}
	if model := findModelByID(GetMetaModels(), overlayTestMetaModelID); model == nil || model.DisplayName != "Local Meta Test" {
		t.Fatalf("meta model = %+v, want the local overlay definition", model)
	}

	ApplyModelOverlayFile("")
	if found := findModelByID(GetMetaModels(), overlayTestMetaModelID); found != nil {
		t.Fatalf("%s remained after clearing the overlay", overlayTestMetaModelID)
	}
}

func TestStoreRefreshedModelsCatalogKeepsMetaOverlayOutOfBase(t *testing.T) {
	restoreModelCatalog(t)

	metaModels := GetMetaModels()
	if len(metaModels) == 0 {
		t.Fatal("embedded Meta catalog is empty")
	}
	baseModelID := metaModels[0].ID

	ApplyModelOverlayFile(writeOverlayFile(t, overlayTestMetaJSON))
	refreshed := *getBaseModels()
	refreshed.Meta = nil
	storeRefreshedModelsCatalog(&refreshed)

	if findModelByID(GetMetaModels(), overlayTestMetaModelID) == nil {
		t.Fatalf("%s was dropped by a catalog refresh", overlayTestMetaModelID)
	}
	if findModelByID(GetMetaModels(), baseModelID) == nil {
		t.Fatalf("base Meta model %s was dropped by a catalog refresh", baseModelID)
	}

	ApplyModelOverlayFile("")
	if found := findModelByID(GetMetaModels(), overlayTestMetaModelID); found != nil {
		t.Fatalf("%s was promoted into the base catalog", overlayTestMetaModelID)
	}
	if findModelByID(GetMetaModels(), baseModelID) == nil {
		t.Fatalf("base Meta model %s was dropped after clearing the overlay", baseModelID)
	}
}

func TestApplyModelOverlayFileUnreadableIsNoOp(t *testing.T) {
	restoreModelCatalog(t)

	baseline := GetCodexProModels()

	missing := filepath.Join(t.TempDir(), "absent.json")
	if changed := ApplyModelOverlayFile(missing); len(changed) != 0 {
		t.Fatalf("missing overlay reported changes: %v", changed)
	}
	if got := GetCodexProModels(); len(got) != len(baseline) {
		t.Fatalf("codex-pro model count = %d, want %d", len(got), len(baseline))
	}

	invalid := writeOverlayFile(t, `{"codex": [`)
	if changed := ApplyModelOverlayFile(invalid); len(changed) != 0 {
		t.Fatalf("unparsable overlay reported changes: %v", changed)
	}
	if got := GetCodexProModels(); len(got) != len(baseline) {
		t.Fatalf("codex-pro model count after unparsable overlay = %d, want %d", len(got), len(baseline))
	}

	duplicate := writeOverlayFile(t, `{"codex": [{"id": "dup"}, {"id": "dup"}]}`)
	if changed := ApplyModelOverlayFile(duplicate); len(changed) != 0 {
		t.Fatalf("overlay with duplicate ids reported changes: %v", changed)
	}
	if findModelByID(GetCodexProModels(), "dup") != nil {
		t.Fatal("overlay with duplicate ids was applied")
	}
}
