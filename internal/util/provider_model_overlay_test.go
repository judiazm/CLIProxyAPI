package util

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// TestGetProviderNameResolvesModelsFileOverlayModel covers the lookup behind the
// "unknown provider for model" request error: a Codex credential must be able to
// serve a model that only the local models-file overlay declares.
func TestGetProviderNameResolvesModelsFileOverlayModel(t *testing.T) {
	const modelID = "gpt-daybreak-blue-latest"
	const clientID = "models-file-overlay-test-client"

	overlay := `{
  "codex": [
    {
      "id": "gpt-daybreak-blue-latest",
      "object": "model",
      "created": 1770912000,
      "owned_by": "openai",
      "type": "openai",
      "display_name": "Daybreak Blue",
      "context_length": 272000,
      "max_completion_tokens": 128000
    }
  ]
}`
	path := filepath.Join(t.TempDir(), "models.local.json")
	if errWrite := os.WriteFile(path, []byte(overlay), 0o600); errWrite != nil {
		t.Fatalf("write overlay file: %v", errWrite)
	}

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
		registry.ApplyModelOverlayFile("")
	})

	// Without the overlay the model is unknown, which is what produces the request error.
	if providers := GetProviderName(modelID); len(providers) != 0 {
		t.Fatalf("GetProviderName(%s) = %v before the overlay, want none", modelID, providers)
	}

	registry.ApplyModelOverlayFile(path)
	registry.GetGlobalRegistry().RegisterClient(clientID, "codex", registry.GetCodexProModels())

	providers := GetProviderName(modelID)
	if !slices.Contains(providers, "codex") {
		t.Fatalf("GetProviderName(%s) = %v, want it to contain codex", modelID, providers)
	}
}
