// model_overlay.go implements the local model catalog overlay configured through
// the top-level `models-file` config key. The overlay is merged on top of the
// embedded catalog and re-applied after every remote refresh so a refresh never
// drops locally declared models.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// modelOverlayJSON mirrors the top-level structure of models.json. It adds a
// "codex" channel that is a shorthand for every Codex plan tier, so an entry
// declared there is served regardless of the plan a Codex credential reports.
type modelOverlayJSON struct {
	Claude      []*ModelInfo `json:"claude"`
	Gemini      []*ModelInfo `json:"gemini"`
	Vertex      []*ModelInfo `json:"vertex"`
	AIStudio    []*ModelInfo `json:"aistudio"`
	Codex       []*ModelInfo `json:"codex"`
	CodexFree   []*ModelInfo `json:"codex-free"`
	CodexTeam   []*ModelInfo `json:"codex-team"`
	CodexPlus   []*ModelInfo `json:"codex-plus"`
	CodexPro    []*ModelInfo `json:"codex-pro"`
	Kimi        []*ModelInfo `json:"kimi"`
	Antigravity []*ModelInfo `json:"antigravity"`
	XAI         []*ModelInfo `json:"xai"`
}

// overlayStore holds the configured overlay path and the last successfully parsed overlay.
type overlayStore struct {
	mu   sync.RWMutex
	path string
	data *modelOverlayJSON
}

var modelOverlayStore = &overlayStore{}

// ApplyModelOverlayFile records path as the active local model catalog overlay,
// reloads it, and re-applies it on top of the current base catalog. An empty
// path clears the overlay. A missing or unparsable file is reported as a warning
// and leaves the effective catalog unchanged.
//
// It returns the provider names whose effective model definitions changed, using
// the same provider naming as the remote refresh change detection.
func ApplyModelOverlayFile(path string) []string {
	path = strings.TrimSpace(path)

	modelOverlayStore.mu.Lock()
	modelOverlayStore.path = path
	if path == "" {
		modelOverlayStore.data = nil
	} else if parsed, errLoad := loadModelOverlayFile(path); errLoad != nil {
		log.Warnf("registry: model catalog overlay %s not applied, keeping the current catalog: %v", path, errLoad)
	} else {
		modelOverlayStore.data = parsed
		log.Infof("registry: model catalog overlay loaded from %s", path)
	}
	modelOverlayStore.mu.Unlock()

	return reapplyModelOverlay()
}

// NotifyModelCatalogChange delivers changedProviders to the registered model
// refresh callback so existing auth registrations pick up the new definitions.
func NotifyModelCatalogChange(changedProviders []string) {
	notifyModelRefresh(changedProviders)
}

// currentModelOverlay returns the last successfully parsed overlay, or nil when none is active.
func currentModelOverlay() *modelOverlayJSON {
	modelOverlayStore.mu.RLock()
	defer modelOverlayStore.mu.RUnlock()
	return modelOverlayStore.data
}

// reapplyModelOverlay recomputes the effective catalog from the stored base
// catalog and the current overlay, returning the providers that changed.
func reapplyModelOverlay() []string {
	overlay := currentModelOverlay()

	modelsCatalogStore.mu.Lock()
	previous := modelsCatalogStore.data
	effective := mergeModelOverlay(modelsCatalogStore.base, overlay)
	modelsCatalogStore.data = effective
	modelsCatalogStore.mu.Unlock()

	return detectChangedProviders(previous, effective)
}

// mergeModelOverlay returns base with the overlay entries merged in. Entries whose
// id already exists replace the base definition in place; new ids are appended to
// their channel. The base catalog itself is never modified.
func mergeModelOverlay(base *staticModelsJSON, overlay *modelOverlayJSON) *staticModelsJSON {
	if base == nil || overlay == nil {
		return base
	}

	merged := *base
	merged.Claude = overlayModelInfos(base.Claude, overlay.Claude)
	merged.Gemini = overlayModelInfos(base.Gemini, overlay.Gemini)
	merged.Vertex = overlayModelInfos(base.Vertex, overlay.Vertex)
	merged.AIStudio = overlayModelInfos(base.AIStudio, overlay.AIStudio)
	merged.CodexFree = overlayModelInfos(base.CodexFree, overlay.Codex, overlay.CodexFree)
	merged.CodexTeam = overlayModelInfos(base.CodexTeam, overlay.Codex, overlay.CodexTeam)
	merged.CodexPlus = overlayModelInfos(base.CodexPlus, overlay.Codex, overlay.CodexPlus)
	merged.CodexPro = overlayModelInfos(base.CodexPro, overlay.Codex, overlay.CodexPro)
	merged.Kimi = overlayModelInfos(base.Kimi, overlay.Kimi)
	merged.Antigravity = overlayModelInfos(base.Antigravity, overlay.Antigravity)
	merged.XAI = overlayModelInfos(base.XAI, overlay.XAI)
	return &merged
}

// overlayModelInfos merges the overlay lists into base. Matching ids (compared
// case-insensitively) are replaced where they already sit, unknown ids are
// appended. base is returned unchanged when the overlay contributes nothing.
func overlayModelInfos(base []*ModelInfo, overlays ...[]*ModelInfo) []*ModelInfo {
	applied := false
	for _, overlay := range overlays {
		for _, model := range overlay {
			if model != nil && strings.TrimSpace(model.ID) != "" {
				applied = true
				break
			}
		}
		if applied {
			break
		}
	}
	if !applied {
		return base
	}

	positions := make(map[string]int, len(base))
	out := make([]*ModelInfo, 0, len(base)+len(overlays))
	for _, model := range base {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if position, exists := positions[key]; exists {
			out[position] = model
			continue
		}
		positions[key] = len(out)
		out = append(out, model)
	}

	for _, overlay := range overlays {
		for _, model := range overlay {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			key := strings.ToLower(id)
			if position, exists := positions[key]; exists {
				out[position] = cloneModelInfo(model)
				continue
			}
			positions[key] = len(out)
			out = append(out, cloneModelInfo(model))
		}
	}
	return out
}

// loadModelOverlayFile reads and validates the overlay file at path.
func loadModelOverlayFile(path string) (*modelOverlayJSON, error) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("read model catalog overlay: %w", errRead)
	}

	var parsed modelOverlayJSON
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		return nil, fmt.Errorf("decode model catalog overlay: %w", errUnmarshal)
	}
	if errValidate := validateModelOverlay(&parsed); errValidate != nil {
		return nil, errValidate
	}
	return &parsed, nil
}

// validateModelOverlay rejects overlay channels containing null entries, empty ids, or duplicates.
func validateModelOverlay(overlay *modelOverlayJSON) error {
	if overlay == nil {
		return fmt.Errorf("overlay is nil")
	}

	sections := []struct {
		name   string
		models []*ModelInfo
	}{
		{name: "claude", models: overlay.Claude},
		{name: "gemini", models: overlay.Gemini},
		{name: "vertex", models: overlay.Vertex},
		{name: "aistudio", models: overlay.AIStudio},
		{name: "codex", models: overlay.Codex},
		{name: "codex-free", models: overlay.CodexFree},
		{name: "codex-team", models: overlay.CodexTeam},
		{name: "codex-plus", models: overlay.CodexPlus},
		{name: "codex-pro", models: overlay.CodexPro},
		{name: "kimi", models: overlay.Kimi},
		{name: "antigravity", models: overlay.Antigravity},
		{name: "xai", models: overlay.XAI},
	}

	for _, section := range sections {
		seen := make(map[string]struct{}, len(section.models))
		for i, model := range section.models {
			if model == nil {
				return fmt.Errorf("%s[%d] is null", section.name, i)
			}
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				return fmt.Errorf("%s[%d] has empty id", section.name, i)
			}
			key := strings.ToLower(modelID)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%s contains duplicate model id %q", section.name, modelID)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}
