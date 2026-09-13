package usagestore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// rowColumns is every persisted column in Row field order, used by the requests endpoint.
var rowColumns = []string{
	"id", "ts_ms", "api_key", "model", "alias", "auth_id", "auth_index", "provider",
	"auth_type", "executor_type", "endpoint", "request_id", "session_id", "parent_session_id",
	"reasoning_effort", "service_tier", "response_service_tier", "stream", "generate",
	"failed", "status_code", "latency_ms", "ttft_ms", "input_tokens", "output_tokens",
	"reasoning_tokens", "cached_tokens", "cache_read_tokens", "cache_creation_tokens",
	"total_tokens", "client_ip", "user_agent",
}

// RequestsResult is the response payload of the requests endpoint.
type RequestsResult struct {
	Rows       []*Row `json:"rows"`
	NextBefore *int64 `json:"next_before"`
}

// Requests returns the newest matching rows, ordered by ts_ms DESC then id DESC.
// A non-nil before restricts the page to rows with a smaller id, acting as a cursor.
func (s *Store) Requests(ctx context.Context, filter Filter, limit int, before *int64, loc *time.Location) (*RequestsResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usagestore: store unavailable")
	}
	if loc == nil {
		loc = time.UTC
	}
	if limit <= 0 {
		limit = 1
	}

	predicate, args := filter.where()
	if before != nil {
		predicate += " AND id < ?"
		args = append(args, *before)
	}
	query := "SELECT " + strings.Join(rowColumns, ", ") +
		" FROM usage_requests WHERE " + predicate +
		" ORDER BY ts_ms DESC, id DESC LIMIT ?"
	args = append(args, limit)

	sqlRows, errQuery := s.db.QueryContext(ctx, query, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestore: requests query: %w", errQuery)
	}
	defer func() {
		if errClose := sqlRows.Close(); errClose != nil {
			log.Errorf("usage store: close requests rows failed: %v", errClose)
		}
	}()

	result := &RequestsResult{Rows: make([]*Row, 0, limit)}
	for sqlRows.Next() {
		row := &Row{}
		var stream, generate, failed int64
		if errScan := sqlRows.Scan(
			&row.ID, &row.TsMs, &row.APIKey, &row.Model, &row.Alias, &row.AuthID,
			&row.AuthIndex, &row.Provider, &row.AuthType, &row.ExecutorType, &row.Endpoint,
			&row.RequestID, &row.SessionID, &row.ParentSessionID, &row.ReasoningEffort,
			&row.ServiceTier, &row.ResponseServiceTier, &stream, &generate, &failed,
			&row.StatusCode, &row.LatencyMs, &row.TTFTMs, &row.InputTokens, &row.OutputTokens,
			&row.ReasoningTokens, &row.CachedTokens, &row.CacheReadTokens,
			&row.CacheCreationTokens, &row.TotalTokens, &row.ClientIP, &row.UserAgent,
		); errScan != nil {
			return nil, fmt.Errorf("usagestore: scan request row: %w", errScan)
		}
		row.Stream = stream != 0
		row.Generate = generate != 0
		row.Failed = failed != 0
		row.Ts = time.UnixMilli(row.TsMs).In(loc).Format(time.RFC3339)
		result.Rows = append(result.Rows, row)
	}
	if errRows := sqlRows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestore: iterate request rows: %w", errRows)
	}

	if len(result.Rows) == limit {
		cursor := result.Rows[len(result.Rows)-1].ID
		result.NextBefore = &cursor
	}
	return result, nil
}

// MetaDimensionColumns are the columns the meta endpoint reports distinct values for.
var MetaDimensionColumns = []string{
	"api_key", "model", "alias", "auth_id", "provider", "auth_type",
	"reasoning_effort", "endpoint",
}

// MetaDimensionDays bounds how far back distinct dimension values are collected.
const MetaDimensionDays = 90

// Meta is the response payload of the meta endpoint.
type Meta struct {
	Enabled       bool                `json:"enabled"`
	Path          string              `json:"path"`
	RetentionDays int                 `json:"retention_days"`
	Rows          int64               `json:"rows"`
	Oldest        *string             `json:"oldest"`
	Newest        *string             `json:"newest"`
	SizeBytes     int64               `json:"size_bytes"`
	Dimensions    map[string][]string `json:"dimensions"`
}

// Meta reports store status, row bounds, file size and the distinct dimension values
// observed in the last MetaDimensionDays days.
func (s *Store) Meta(ctx context.Context, loc *time.Location) (*Meta, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usagestore: store unavailable")
	}
	if loc == nil {
		loc = time.UTC
	}

	meta := &Meta{
		Enabled:       true,
		Path:          s.path,
		RetentionDays: s.retentionDays,
		Dimensions:    make(map[string][]string, len(MetaDimensionColumns)),
	}

	var oldest, newest sql.NullInt64
	if errScan := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*), MIN(ts_ms), MAX(ts_ms) FROM usage_requests",
	).Scan(&meta.Rows, &oldest, &newest); errScan != nil {
		return nil, fmt.Errorf("usagestore: meta counts: %w", errScan)
	}
	if oldest.Valid {
		value := time.UnixMilli(oldest.Int64).In(loc).Format(time.RFC3339)
		meta.Oldest = &value
	}
	if newest.Valid {
		value := time.UnixMilli(newest.Int64).In(loc).Format(time.RFC3339)
		meta.Newest = &value
	}

	if info, errStat := os.Stat(s.path); errStat == nil {
		meta.SizeBytes = info.Size()
	}

	cutoff := time.Now().AddDate(0, 0, -MetaDimensionDays).UnixMilli()
	for _, column := range MetaDimensionColumns {
		values, errDistinct := s.distinct(ctx, column, cutoff)
		if errDistinct != nil {
			return nil, errDistinct
		}
		meta.Dimensions[column] = values
	}
	return meta, nil
}

// distinct lists the non-empty distinct values of column since cutoff, sorted ascending.
func (s *Store) distinct(ctx context.Context, column string, cutoff int64) ([]string, error) {
	query := "SELECT DISTINCT " + column + " FROM usage_requests WHERE ts_ms >= ? AND " +
		column + " != '' ORDER BY " + column + " ASC"
	rows, errQuery := s.db.QueryContext(ctx, query, cutoff)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestore: distinct %s: %w", column, errQuery)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.Errorf("usage store: close distinct rows failed: %v", errClose)
		}
	}()

	values := make([]string, 0, 16)
	for rows.Next() {
		var value string
		if errScan := rows.Scan(&value); errScan != nil {
			return nil, fmt.Errorf("usagestore: scan distinct %s: %w", column, errScan)
		}
		values = append(values, value)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestore: iterate distinct %s: %w", column, errRows)
	}
	return values, nil
}

// Count returns the number of stored rows. It exists for tests and diagnostics.
func (s *Store) Count(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("usagestore: store unavailable")
	}
	var count int64
	if errScan := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_requests").Scan(&count); errScan != nil {
		return 0, fmt.Errorf("usagestore: count rows: %w", errScan)
	}
	return count, nil
}
