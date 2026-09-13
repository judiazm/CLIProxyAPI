package usagestore

import (
	"context"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// PluginName is the name the store is registered under on the default usage manager.
const PluginName = "usage-store"

func init() {
	coreusage.RegisterNamedPlugin(PluginName, &storePlugin{})
}

type storePlugin struct{}

// HandleUsage converts a usage record into a row and hands it to the active store.
// It is a no-op while the store is disabled.
func (p *storePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	store := Current()
	if store == nil {
		return
	}
	store.Enqueue(RowFromRecord(ctx, record))
}

// RowFromRecord resolves every column from a usage record. Field resolution deliberately
// mirrors internal/redisqueue/plugin.go so both usage sinks report the same values.
func RowFromRecord(ctx context.Context, record coreusage.Record) *Row {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	aliasName := strings.TrimSpace(record.Alias)
	if aliasName == "" {
		aliasName = modelName
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	executorType := strings.TrimSpace(record.ExecutorType)
	if executorType == "" {
		executorType = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}

	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = strings.TrimSpace(record.RequestServiceTier)
	}
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}

	clientRequestMetadata := internallogging.GetClientRequestMetadata(ctx)
	sessionID := strings.TrimSpace(record.SessionID)
	parentSessionID := strings.TrimSpace(record.ParentSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(clientRequestMetadata.SessionID)
		parentSessionID = strings.TrimSpace(clientRequestMetadata.ParentSessionID)
	} else if parentSessionID == "" && sessionID == strings.TrimSpace(clientRequestMetadata.SessionID) {
		parentSessionID = strings.TrimSpace(clientRequestMetadata.ParentSessionID)
	}
	sessionID = coresession.NormalizeToCanonicalUUID(sessionID)
	parentSessionID = coresession.NormalizeToCanonicalUUID(parentSessionID)
	if sessionID == "" || sessionID == parentSessionID {
		parentSessionID = ""
	}

	usageDetail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	statusCode := 0
	if failed {
		statusCode = resolveFailStatus(ctx, record)
	}

	stream := record.Stream
	if !stream {
		stream = coreusage.StreamFromContext(ctx)
	}

	return &Row{
		TsMs:                timestamp.UnixMilli(),
		APIKey:              strings.TrimSpace(record.APIKey),
		Model:               modelName,
		Alias:               aliasName,
		AuthID:              record.AuthID,
		AuthIndex:           record.AuthIndex,
		Provider:            provider,
		AuthType:            authType,
		ExecutorType:        executorType,
		Endpoint:            resolveEndpoint(ctx),
		RequestID:           strings.TrimSpace(internallogging.GetRequestID(ctx)),
		SessionID:           sessionID,
		ParentSessionID:     parentSessionID,
		ReasoningEffort:     reasoningEffort,
		ServiceTier:         serviceTier,
		ResponseServiceTier: strings.TrimSpace(record.ResponseServiceTier),
		Stream:              stream,
		Generate:            coreusage.GenerateEnabled(record.Generate),
		Failed:              failed,
		StatusCode:          int64(statusCode),
		LatencyMs:           record.Latency.Milliseconds(),
		TTFTMs:              record.TTFT.Milliseconds(),
		InputTokens:         usageDetail.InputTokens,
		OutputTokens:        usageDetail.OutputTokens,
		ReasoningTokens:     usageDetail.ReasoningTokens,
		CachedTokens:        usageDetail.CachedTokens,
		CacheReadTokens:     usageDetail.CacheReadTokens,
		CacheCreationTokens: usageDetail.CacheCreationTokens,
		TotalTokens:         usageDetail.TotalTokens,
		ClientIP:            clientRequestMetadata.ClientIP,
		UserAgent:           clientRequestMetadata.UserAgent,
	}
}

const httpStatusBadRequest = 400

// resolveSuccess mirrors redisqueue.resolveSuccess.
func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

// resolveFailStatus mirrors the status-code half of redisqueue.resolveFail. The failure
// body is deliberately not persisted.
func resolveFailStatus(ctx context.Context, record coreusage.Record) int {
	statusCode := record.Fail.StatusCode
	if statusCode <= 0 {
		statusCode = internallogging.GetResponseStatus(ctx)
	}
	if statusCode <= 0 {
		statusCode = 500
	}
	return statusCode
}

// resolveEndpoint mirrors redisqueue.resolveEndpoint.
func resolveEndpoint(ctx context.Context) string {
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}
