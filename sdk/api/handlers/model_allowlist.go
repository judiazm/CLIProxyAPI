package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

// modelListIDFields are the per-entry fields that carry a model ID across the list shapes this
// proxy serves: "id" (OpenAI and Claude), "name" (Gemini) and "slug" (Codex client catalog).
var modelListIDFields = []string{"id", "name", "slug"}

// modelListArrayFields are the arrays that hold model entries in those same shapes.
var modelListArrayFields = []string{"data", "models"}

// allowedModelsForKey returns the allow patterns configured for a client key, or nil when the key
// is unknown or unrestricted.
func (h *BaseAPIHandler) allowedModelsForKey(apiKey string) []string {
	if h == nil || h.Cfg == nil {
		return nil
	}
	return h.Cfg.APIKeys.AllowedModelsFor(apiKey)
}

// allowedModelsForGin returns the allow patterns for the key that authenticated this request.
func (h *BaseAPIHandler) allowedModelsForGin(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	return h.allowedModelsForKey(c.GetString("userApiKey"))
}

// allowedModelsForContext returns the allow patterns for the key that authenticated the request
// the context belongs to. Executions without an inbound HTTP request are unrestricted.
func (h *BaseAPIHandler) allowedModelsForContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return nil
	}
	return h.allowedModelsForGin(ginCtx)
}

// enforceModelAllowlist rejects a request whose model the caller's key is not allowed to use.
// Nested plugin-host executions are exempt: they run on behalf of the proxy, not the client.
func (h *BaseAPIHandler) enforceModelAllowlist(ctx context.Context, modelName string, execOptions modelExecutionOptions) *interfaces.ErrorMessage {
	if execOptions.InternalSource {
		return nil
	}
	patterns := h.allowedModelsForContext(ctx)
	if len(patterns) == 0 {
		return nil
	}
	if modelMatchesAllowList(modelName, patterns) {
		return nil
	}
	return modelNotAllowedError(modelName)
}

// modelNotAllowedError mirrors the error shape used when a model cannot be routed, so clients
// treat a blocked model exactly like an unknown one.
func modelNotAllowedError(modelName string) *interfaces.ErrorMessage {
	// The model name is client supplied, so it is inserted through sjson rather than formatted
	// into the JSON literal: an unescaped quote would otherwise corrupt the body or let the
	// caller overwrite the error code.
	body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
	body, errSet := sjson.Set(body, "error.message", "model not allowed for this API key: "+modelName)
	if errSet != nil {
		body = `{"error":{"message":"model not allowed for this API key","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
	}
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(body),
	}
}

// modelMatchesAllowList reports whether a model ID matches any allow pattern. An empty pattern
// list means the key is unrestricted.
func modelMatchesAllowList(modelName string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	candidates := modelMatchCandidates(modelName)
	if len(candidates) == 0 {
		return false
	}
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			if util.MatchWildcard(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

// modelMatchCandidates expands a model ID into the forms a configured pattern may target:
// the ID as listed (credential prefix included), without the Gemini "models/" path prefix,
// with Claude model ID cloaking reversed, and without a thinking suffix.
func modelMatchCandidates(modelName string) []string {
	base := strings.ToLower(strings.TrimSpace(modelName))
	if base == "" {
		return nil
	}
	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		candidates = append(candidates, value)
	}

	add(base)
	add(strings.TrimPrefix(base, "models/"))
	for _, candidate := range append([]string(nil), candidates...) {
		add(claudemodels.ResolveClaudeModelIDPrefix(candidate))
	}
	for _, candidate := range append([]string(nil), candidates...) {
		add(thinking.ParseSuffix(candidate).ModelName)
	}
	// A cloaked ID (a non-Claude model disguised under a claude-* name for Claude
	// clients) must only match through its real ID; otherwise every disguised
	// model satisfies a "claude-*" allowlist.
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if claudemodels.ResolveClaudeModelIDPrefix(candidate) != candidate {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

// applyModelListAllowlist drops model entries the caller's key may not use from a model-list
// payload. Payload shapes without a recognizable model array are returned unchanged.
func (h *BaseAPIHandler) applyModelListAllowlist(c *gin.Context, body []byte) []byte {
	return filterModelListPayload(body, h.allowedModelsForGin(c))
}

func filterModelListPayload(body []byte, patterns []string) []byte {
	if len(patterns) == 0 || len(body) == 0 {
		return body
	}
	for _, field := range modelListArrayFields {
		array := gjson.GetBytes(body, field)
		if !array.IsArray() {
			continue
		}
		entries := array.Array()
		kept := make([]string, 0, len(entries))
		dropped := false
		for _, entry := range entries {
			id := modelListEntryID(entry)
			// Entries without a recognizable model ID name no model the client could select,
			// so they are left in place rather than silently removed.
			if id == "" || modelMatchesAllowList(id, patterns) {
				kept = append(kept, entry.Raw)
				continue
			}
			dropped = true
		}
		if !dropped {
			continue
		}
		updated, errSet := sjson.SetRawBytes(body, field, []byte("["+strings.Join(kept, ",")+"]"))
		if errSet != nil {
			continue
		}
		body = updated
		body = updateModelListCursors(body, kept)
	}
	return body
}

// modelListEntryID reads the model ID of one list entry.
func modelListEntryID(entry gjson.Result) string {
	for _, field := range modelListIDFields {
		if value := strings.TrimSpace(entry.Get(field).String()); value != "" {
			return value
		}
	}
	return ""
}

// updateModelListCursors keeps the Anthropic pagination cursors consistent with a filtered list.
func updateModelListCursors(body []byte, kept []string) []byte {
	firstID := ""
	lastID := ""
	if len(kept) > 0 {
		firstID = gjson.Get(kept[0], "id").String()
		lastID = gjson.Get(kept[len(kept)-1], "id").String()
	}
	body = setExistingStringField(body, "first_id", firstID)
	body = setExistingStringField(body, "last_id", lastID)
	return body
}

// setExistingStringField updates a top-level string field only when the payload already has it.
func setExistingStringField(body []byte, field, value string) []byte {
	if !gjson.GetBytes(body, field).Exists() {
		return body
	}
	updated, errSet := sjson.SetBytes(body, field, value)
	if errSet != nil {
		return body
	}
	return updated
}
