package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// registerAllowedModelsCatalog publishes two OpenAI-channel models for the duration of a test.
func registerAllowedModelsCatalog(t *testing.T) {
	t.Helper()
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-allowed-models"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: "gpt-6-astra", DisplayName: "GPT-6 Astra"},
		{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1"},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
}

func listModelIDs(t *testing.T, server *Server, apiKey string) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://proxy.example.test/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	ids := make([]string, 0, len(response.Data))
	for _, model := range response.Data {
		ids = append(ids, model.ID)
	}
	return ids
}

func containsModelID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestModelListFilteredByAllowedModels(t *testing.T) {
	registerAllowedModelsCatalog(t)

	server := newTestServer(t)
	cfg := *server.cfg
	cfg.APIKeys = sdkconfig.APIKeyEntries{
		{APIKey: "test-key"},
		{APIKey: "codex-key", AllowedModels: []string{"gpt-*"}},
	}
	server.UpdateClients(&cfg)

	restricted := listModelIDs(t, server, "codex-key")
	if !containsModelID(restricted, "gpt-6-astra") {
		t.Fatalf("restricted list = %#v, want gpt-6-astra", restricted)
	}
	if containsModelID(restricted, "claude-opus-4-1") {
		t.Fatalf("restricted list = %#v, want claude-opus-4-1 filtered out", restricted)
	}

	unrestricted := listModelIDs(t, server, "test-key")
	if !containsModelID(unrestricted, "gpt-6-astra") || !containsModelID(unrestricted, "claude-opus-4-1") {
		t.Fatalf("unrestricted list = %#v, want the full catalog", unrestricted)
	}
}

func TestModelListAllowedModelsReloadWithoutRestart(t *testing.T) {
	registerAllowedModelsCatalog(t)

	server := newTestServer(t)
	cfg := *server.cfg
	cfg.APIKeys = sdkconfig.APIKeyEntries{
		{APIKey: "codex-key", AllowedModels: []string{"gpt-*"}},
	}
	server.UpdateClients(&cfg)

	if ids := listModelIDs(t, server, "codex-key"); containsModelID(ids, "claude-opus-4-1") {
		t.Fatalf("list = %#v, want claude-opus-4-1 filtered out", ids)
	}

	// Same server instance, new configuration: this is what the config watcher applies.
	reloaded := cfg
	reloaded.APIKeys = sdkconfig.APIKeyEntries{
		{APIKey: "codex-key", AllowedModels: []string{"claude-*"}},
	}
	server.UpdateClients(&reloaded)

	ids := listModelIDs(t, server, "codex-key")
	if !containsModelID(ids, "claude-opus-4-1") {
		t.Fatalf("list after reload = %#v, want claude-opus-4-1", ids)
	}
	if containsModelID(ids, "gpt-6-astra") {
		t.Fatalf("list after reload = %#v, want gpt-6-astra filtered out", ids)
	}
}

func TestChatCompletionsRejectsModelOutsideAllowList(t *testing.T) {
	registerAllowedModelsCatalog(t)

	server := newTestServer(t)
	cfg := *server.cfg
	cfg.APIKeys = sdkconfig.APIKeyEntries{
		{APIKey: "codex-key", AllowedModels: []string{"gpt-*"}},
	}
	server.UpdateClients(&cfg)

	body := `{"model":"claude-opus-4-1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "https://proxy.example.test/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer codex-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	var response struct {
		Error struct {
			Code  string `json:"code"`
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if response.Error.Code != "model_not_found" {
		t.Fatalf("error.code = %q, want model_not_found; body=%s", response.Error.Code, recorder.Body.String())
	}
	if response.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error", response.Error.Type)
	}
	if response.Error.Param != "model" {
		t.Fatalf("error.param = %q, want model", response.Error.Param)
	}
}
