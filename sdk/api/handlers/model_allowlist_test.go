package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func restrictedHandler(key string, allowed ...string) *BaseAPIHandler {
	return &BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{
		APIKeys: sdkconfig.APIKeyEntries{
			{APIKey: "unrestricted-key"},
			{APIKey: key, AllowedModels: allowed},
		},
	}}
}

func restrictedGinContext(t *testing.T, key, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	ctx.Set("userApiKey", key)
	return ctx, rec
}

func TestFilterModelListPayloadOpenAIShape(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-6-astra"},{"id":"natacha/gpt-6-astra"},{"id":"claude-sonnet-4-5"}]}`)

	filtered := filterModelListPayload(body, []string{"gpt-*"})

	ids := modelListIDs(t, filtered, "data")
	if want := []string{"gpt-6-astra"}; !equalStrings(ids, want) {
		t.Fatalf("data ids = %#v, want %#v", ids, want)
	}
	if gjson.GetBytes(filtered, "object").String() != "list" {
		t.Fatalf("filtered payload lost its envelope: %s", filtered)
	}
}

func TestFilterModelListPayloadMatchesCredentialPrefix(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"natacha/gpt-6-astra"},{"id":"gpt-6-astra"}]}`)

	filtered := filterModelListPayload(body, []string{"natacha/*"})

	ids := modelListIDs(t, filtered, "data")
	if want := []string{"natacha/gpt-6-astra"}; !equalStrings(ids, want) {
		t.Fatalf("data ids = %#v, want %#v", ids, want)
	}
}

func TestFilterModelListPayloadClaudeShape(t *testing.T) {
	cloaked := claudemodels.EnsureClaudeModelIDPrefix("gpt-6-astra")
	body := []byte(`{"data":[{"id":"claude-sonnet-4-5"},{"id":"` + cloaked + `"}],"has_more":false,"first_id":"claude-sonnet-4-5","last_id":"` + cloaked + `"}`)

	// Cloaked Claude IDs are resolved before matching, so a gpt-* key keeps seeing its models.
	filtered := filterModelListPayload(body, []string{"gpt-*"})

	ids := modelListIDs(t, filtered, "data")
	if want := []string{cloaked}; !equalStrings(ids, want) {
		t.Fatalf("data ids = %#v, want %#v", ids, want)
	}
	if got := gjson.GetBytes(filtered, "first_id").String(); got != cloaked {
		t.Fatalf("first_id = %q, want %q", got, cloaked)
	}
	if got := gjson.GetBytes(filtered, "last_id").String(); got != cloaked {
		t.Fatalf("last_id = %q, want %q", got, cloaked)
	}
}

func TestFilterModelListPayloadClearsCursorsWhenEmpty(t *testing.T) {
	body := []byte(`{"data":[{"id":"claude-sonnet-4-5"}],"has_more":false,"first_id":"claude-sonnet-4-5","last_id":"claude-sonnet-4-5"}`)

	filtered := filterModelListPayload(body, []string{"gpt-*"})

	if ids := modelListIDs(t, filtered, "data"); len(ids) != 0 {
		t.Fatalf("data ids = %#v, want empty", ids)
	}
	if got := gjson.GetBytes(filtered, "first_id").String(); got != "" {
		t.Fatalf("first_id = %q, want empty", got)
	}
}

func TestFilterModelListPayloadGeminiShape(t *testing.T) {
	body := []byte(`{"models":[{"name":"models/gemini-3-pro-preview"},{"name":"models/gpt-6-astra"}]}`)

	filtered := filterModelListPayload(body, []string{"gemini-*"})

	names := modelListIDs(t, filtered, "models")
	if want := []string{"models/gemini-3-pro-preview"}; !equalStrings(names, want) {
		t.Fatalf("model names = %#v, want %#v", names, want)
	}
}

func TestFilterModelListPayloadCodexClientShape(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-6-astra"},{"slug":"claude-sonnet-4-5"}]}`)

	filtered := filterModelListPayload(body, []string{"gpt-*"})

	slugs := modelListIDs(t, filtered, "models")
	if want := []string{"gpt-6-astra"}; !equalStrings(slugs, want) {
		t.Fatalf("model slugs = %#v, want %#v", slugs, want)
	}
}

func TestFilterModelListPayloadWithoutPatternsIsUnchanged(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"claude-sonnet-4-5"}]}`)

	if got := filterModelListPayload(body, nil); string(got) != string(body) {
		t.Fatalf("payload = %s, want it unchanged", got)
	}
}

func TestFilterModelListPayloadKeepsUnrecognizedShapes(t *testing.T) {
	body := []byte(`{"catalog":[{"model":"claude-sonnet-4-5"}]}`)

	if got := filterModelListPayload(body, []string{"gpt-*"}); string(got) != string(body) {
		t.Fatalf("payload = %s, want it unchanged", got)
	}
}

func TestWriteModelListResponseFiltersByAllowedModels(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")
	ctx, rec := restrictedGinContext(t, "codex-client-key", "/v1/models")

	payload := gin.H{"object": "list", "data": []map[string]any{
		{"id": "gpt-6-astra"},
		{"id": "claude-sonnet-4-5"},
	}}
	handler.WriteModelListResponse(ctx, "openai", payload)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "gpt-6-astra") {
		t.Fatalf("body = %s, want gpt-6-astra", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "claude-sonnet-4-5") {
		t.Fatalf("body = %s, want claude-sonnet-4-5 filtered out", rec.Body.String())
	}
}

func TestWriteModelListResponseUnrestrictedKeySeesEverything(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")
	ctx, rec := restrictedGinContext(t, "unrestricted-key", "/v1/models")

	payload := gin.H{"object": "list", "data": []map[string]any{
		{"id": "gpt-6-astra"},
		{"id": "claude-sonnet-4-5"},
	}}
	handler.WriteModelListResponse(ctx, "openai", payload)

	if !strings.Contains(rec.Body.String(), "claude-sonnet-4-5") {
		t.Fatalf("body = %s, want the full catalog", rec.Body.String())
	}
}

func TestEnforceModelAllowlist(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")

	tests := []struct {
		name      string
		key       string
		model     string
		internal  bool
		wantBlock bool
	}{
		{name: "allowed model", key: "codex-client-key", model: "gpt-6-astra", wantBlock: false},
		{name: "allowed model with thinking suffix", key: "codex-client-key", model: "gpt-6-astra(8192)", wantBlock: false},
		{name: "blocked model", key: "codex-client-key", model: "claude-sonnet-4-5", wantBlock: true},
		{name: "blocked prefixed model", key: "codex-client-key", model: "natacha/claude-sonnet-4-5", wantBlock: true},
		{name: "unrestricted key", key: "unrestricted-key", model: "claude-sonnet-4-5", wantBlock: false},
		{name: "unknown key", key: "some-other-key", model: "claude-sonnet-4-5", wantBlock: false},
		{name: "plugin host execution", key: "codex-client-key", model: "claude-sonnet-4-5", internal: true, wantBlock: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ginCtx, _ := restrictedGinContext(t, tc.key, "/v1/chat/completions")
			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			errMsg := handler.enforceModelAllowlist(ctx, tc.model, modelExecutionOptions{InternalSource: tc.internal})
			if tc.wantBlock != (errMsg != nil) {
				t.Fatalf("enforceModelAllowlist(%q) error = %v, wantBlock = %t", tc.model, errMsg, tc.wantBlock)
			}
		})
	}
}

func TestEnforceModelAllowlistWithoutRequestContext(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")

	if errMsg := handler.enforceModelAllowlist(context.Background(), "claude-sonnet-4-5", modelExecutionOptions{}); errMsg != nil {
		t.Fatalf("enforceModelAllowlist() error = %v, want nil without an inbound request", errMsg)
	}
}

func TestExecuteWithAuthManagerRejectsDisallowedModel(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")
	ginCtx, _ := restrictedGinContext(t, "codex-client-key", "/v1/messages")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "claude", "claude-sonnet-4-5", []byte(`{"model":"claude-sonnet-4-5"}`), "")
	if errMsg == nil {
		t.Fatal("ExecuteWithAuthManager() error = nil, want model_not_found")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
	body := errMsg.Error.Error()
	if got := gjson.Get(body, "error.code").String(); got != "model_not_found" {
		t.Fatalf("error.code = %q, want model_not_found (body=%s)", got, body)
	}
	if got := gjson.Get(body, "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error (body=%s)", got, body)
	}
	if got := gjson.Get(body, "error.param").String(); got != "model" {
		t.Fatalf("error.param = %q, want model (body=%s)", got, body)
	}
	if got := gjson.Get(body, "error.message").String(); !strings.Contains(got, "claude-sonnet-4-5") {
		t.Fatalf("error.message = %q, want the requested model named", got)
	}
}

func TestExecuteStreamWithAuthManagerRejectsDisallowedModel(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")
	ginCtx, _ := restrictedGinContext(t, "codex-client-key", "/v1/chat/completions")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", "claude-sonnet-4-5", []byte(`{"model":"claude-sonnet-4-5"}`), "")
	if dataChan != nil {
		t.Fatal("ExecuteStreamWithAuthManager() returned a data channel, want none")
	}
	errMsg := <-errChan
	if errMsg == nil {
		t.Fatal("ExecuteStreamWithAuthManager() error = nil, want model_not_found")
	}
	if got := gjson.Get(errMsg.Error.Error(), "error.code").String(); got != "model_not_found" {
		t.Fatalf("error.code = %q, want model_not_found", got)
	}
}

func TestExecuteCountWithAuthManagerRejectsDisallowedModel(t *testing.T) {
	handler := restrictedHandler("codex-client-key", "gpt-*")
	ginCtx, _ := restrictedGinContext(t, "codex-client-key", "/v1/messages/count_tokens")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	_, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, "claude", "claude-sonnet-4-5", []byte(`{"model":"claude-sonnet-4-5"}`), "")
	if errMsg == nil {
		t.Fatal("ExecuteCountWithAuthManager() error = nil, want model_not_found")
	}
}

func modelListIDs(t *testing.T, body []byte, field string) []string {
	t.Helper()
	array := gjson.GetBytes(body, field)
	if !array.IsArray() {
		t.Fatalf("%s is not an array: %s", field, body)
	}
	ids := make([]string, 0, len(array.Array()))
	for _, entry := range array.Array() {
		ids = append(ids, modelListEntryID(entry))
	}
	return ids
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestModelMatchesAllowListIgnoresCloakedClaudeID(t *testing.T) {
	// Codex models are served to Claude clients under a cloaked claude-* ID; the
	// allowlist must see through the disguise in both directions.
	cloaked := claudemodels.EnsureClaudeModelIDPrefix("gpt-5.6-sol")
	if modelMatchesAllowList(cloaked, []string{"claude-*"}) {
		t.Fatalf("cloaked %q must not satisfy claude-*", cloaked)
	}
	if !modelMatchesAllowList(cloaked, []string{"gpt-*"}) {
		t.Fatalf("cloaked %q must satisfy gpt-* through its real ID", cloaked)
	}
	prefixed := claudemodels.EnsureClaudeModelIDPrefix("natacha/gpt-5.6-sol")
	if modelMatchesAllowList(prefixed, []string{"gpt-*"}) {
		t.Fatalf("cloaked %q must not satisfy gpt-* (prefixed)", prefixed)
	}
	if !modelMatchesAllowList(prefixed, []string{"natacha/*"}) {
		t.Fatalf("cloaked %q must satisfy natacha/*", prefixed)
	}
}
