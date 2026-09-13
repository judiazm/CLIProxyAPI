// Package usagestore persists every usage record emitted by the proxy runtime into a
// local SQLite database (modernc.org/sqlite, pure Go) and exposes aggregate queries over
// it. It is enabled by the `usage-store` configuration key and is independent of
// `usage-statistics-enabled`.
package usagestore

// Table is the single table holding one row per request.
const Table = "usage_requests"

const schemaSQL = `
CREATE TABLE IF NOT EXISTS usage_requests (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_ms                 INTEGER NOT NULL,
    api_key               TEXT    NOT NULL DEFAULT '',
    model                 TEXT    NOT NULL DEFAULT '',
    alias                 TEXT    NOT NULL DEFAULT '',
    auth_id               TEXT    NOT NULL DEFAULT '',
    auth_index            TEXT    NOT NULL DEFAULT '',
    provider              TEXT    NOT NULL DEFAULT '',
    auth_type             TEXT    NOT NULL DEFAULT '',
    executor_type         TEXT    NOT NULL DEFAULT '',
    endpoint              TEXT    NOT NULL DEFAULT '',
    request_id            TEXT    NOT NULL DEFAULT '',
    session_id            TEXT    NOT NULL DEFAULT '',
    parent_session_id     TEXT    NOT NULL DEFAULT '',
    reasoning_effort      TEXT    NOT NULL DEFAULT '',
    service_tier          TEXT    NOT NULL DEFAULT '',
    response_service_tier TEXT    NOT NULL DEFAULT '',
    stream                INTEGER NOT NULL DEFAULT 0,
    generate              INTEGER NOT NULL DEFAULT 0,
    failed                INTEGER NOT NULL DEFAULT 0,
    status_code           INTEGER NOT NULL DEFAULT 0,
    latency_ms            INTEGER NOT NULL DEFAULT 0,
    ttft_ms               INTEGER NOT NULL DEFAULT 0,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens      INTEGER NOT NULL DEFAULT 0,
    cached_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens          INTEGER NOT NULL DEFAULT 0,
    client_ip             TEXT    NOT NULL DEFAULT '',
    user_agent            TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_usage_requests_ts ON usage_requests (ts_ms);
CREATE INDEX IF NOT EXISTS idx_usage_requests_api_key_ts ON usage_requests (api_key, ts_ms);
CREATE INDEX IF NOT EXISTS idx_usage_requests_model_ts ON usage_requests (model, ts_ms);
CREATE INDEX IF NOT EXISTS idx_usage_requests_auth_id_ts ON usage_requests (auth_id, ts_ms);
`

// insertSQL inserts one row. Column order matches Row.args.
const insertSQL = `INSERT INTO usage_requests (
    ts_ms, api_key, model, alias, auth_id, auth_index, provider, auth_type, executor_type,
    endpoint, request_id, session_id, parent_session_id, reasoning_effort, service_tier,
    response_service_tier, stream, generate, failed, status_code, latency_ms, ttft_ms,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_read_tokens,
    cache_creation_tokens, total_tokens, client_ip, user_agent
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// Row is one persisted usage record.
type Row struct {
	ID   int64 `json:"id"`
	TsMs int64 `json:"-"`
	// Ts is the RFC3339 rendering of TsMs, populated by read queries only.
	Ts                  string `json:"ts"`
	APIKey              string `json:"api_key"`
	Model               string `json:"model"`
	Alias               string `json:"alias"`
	AuthID              string `json:"auth_id"`
	AuthIndex           string `json:"auth_index"`
	Provider            string `json:"provider"`
	AuthType            string `json:"auth_type"`
	ExecutorType        string `json:"executor_type"`
	Endpoint            string `json:"endpoint"`
	RequestID           string `json:"request_id"`
	SessionID           string `json:"session_id"`
	ParentSessionID     string `json:"parent_session_id"`
	ReasoningEffort     string `json:"reasoning_effort"`
	ServiceTier         string `json:"service_tier"`
	ResponseServiceTier string `json:"response_service_tier"`
	Stream              bool   `json:"stream"`
	Generate            bool   `json:"generate"`
	Failed              bool   `json:"failed"`
	StatusCode          int64  `json:"status_code"`
	LatencyMs           int64  `json:"latency_ms"`
	TTFTMs              int64  `json:"ttft_ms"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	ClientIP            string `json:"client_ip"`
	UserAgent           string `json:"user_agent"`
}

func boolToInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// args returns the insert bind values in insertSQL column order.
func (r *Row) args() []any {
	return []any{
		r.TsMs, r.APIKey, r.Model, r.Alias, r.AuthID, r.AuthIndex, r.Provider, r.AuthType,
		r.ExecutorType, r.Endpoint, r.RequestID, r.SessionID, r.ParentSessionID,
		r.ReasoningEffort, r.ServiceTier, r.ResponseServiceTier, boolToInt(r.Stream),
		boolToInt(r.Generate), boolToInt(r.Failed), r.StatusCode, r.LatencyMs, r.TTFTMs,
		r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CachedTokens, r.CacheReadTokens,
		r.CacheCreationTokens, r.TotalTokens, r.ClientIP, r.UserAgent,
	}
}
