package usagestore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a temp-file database and closes it when the test ends.
func newTestStore(t *testing.T, retentionDays int) *Store {
	t.Helper()
	store, errOpen := Open(Options{
		Path:          filepath.Join(t.TempDir(), "usage.db"),
		RetentionDays: retentionDays,
	})
	if errOpen != nil {
		t.Fatalf("Open: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Errorf("Close: %v", errClose)
		}
	})
	return store
}

func writeRows(t *testing.T, store *Store, rows ...*Row) {
	t.Helper()
	for _, row := range rows {
		store.Enqueue(row)
	}
	if errFlush := store.Flush(context.Background()); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}
}

func TestStoreEnqueuePersistsRow(t *testing.T) {
	store := newTestStore(t, 0)
	at := time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)
	writeRows(t, store, &Row{
		TsMs: at.UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", Alias: "gpt-5.6-sol",
		AuthID: "codex-a.json", Provider: "codex", AuthType: "oauth", Endpoint: "/v1/responses",
		Stream: true, Generate: true, LatencyMs: 1200, TTFTMs: 300,
		InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, TotalTokens: 15,
	})

	count, errCount := store.Count(context.Background())
	if errCount != nil {
		t.Fatalf("Count: %v", errCount)
	}
	if count != 1 {
		t.Fatalf("Count = %d, want 1", count)
	}
}

func TestStoreSummaryGroupedByAPIKeyAndModel(t *testing.T) {
	store := newTestStore(t, 0)
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	writeRows(t, store,
		&Row{TsMs: base.UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", LatencyMs: 100, InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		&Row{TsMs: base.Add(time.Minute).UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", LatencyMs: 300, InputTokens: 20, OutputTokens: 4, TotalTokens: 24, Failed: true},
		&Row{TsMs: base.Add(2 * time.Minute).UnixMilli(), APIKey: "sk-phone", Model: "claude-opus-5", LatencyMs: 50, InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	)

	result, errSummary := store.Summary(context.Background(), SummaryRequest{
		Filter:   Filter{From: base.Add(-time.Hour), To: base.Add(time.Hour)},
		GroupBy:  []string{GroupAPIKey, GroupModel},
		Location: time.UTC,
		OrderBy:  "total_tokens",
		Order:    "desc",
		Limit:    500,
	})
	if errSummary != nil {
		t.Fatalf("Summary: %v", errSummary)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(result.Rows))
	}

	top := result.Rows[0]
	if top.Keys[GroupAPIKey] != "sk-mac" || top.Keys[GroupModel] != "gpt-5.6-sol" {
		t.Fatalf("top keys = %v, want sk-mac/gpt-5.6-sol", top.Keys)
	}
	if top.Requests != 2 || top.Failed != 1 {
		t.Fatalf("top requests/failed = %d/%d, want 2/1", top.Requests, top.Failed)
	}
	if top.TotalTokens != 36 || top.InputTokens != 30 || top.OutputTokens != 6 {
		t.Fatalf("top tokens = %+v, want total 36 input 30 output 6", top.SummaryMetrics)
	}
	if top.LatencyMsAvg != 200 {
		t.Fatalf("top latency avg = %v, want 200", top.LatencyMsAvg)
	}
	if top.LatencyMsP95 == nil || *top.LatencyMsP95 != 300 {
		t.Fatalf("top latency p95 = %v, want 300", top.LatencyMsP95)
	}
	if top.FirstAt == nil || *top.FirstAt != base.Format(time.RFC3339) {
		t.Fatalf("top first_at = %v, want %s", top.FirstAt, base.Format(time.RFC3339))
	}

	if result.Totals.Requests != 3 || result.Totals.TotalTokens != 38 {
		t.Fatalf("totals = %+v, want 3 requests and 38 total tokens", result.Totals)
	}
}

func TestStoreSummaryFiltersAndDayBucketingInNonUTCZone(t *testing.T) {
	store := newTestStore(t, 0)
	location, errLoad := time.LoadLocation("America/New_York")
	if errLoad != nil {
		t.Skipf("zoneinfo unavailable: %v", errLoad)
	}

	// 02:00 UTC on 2026-09-13 is 22:00 on 2026-09-12 in New York.
	crossesMidnight := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
	sameDay := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	writeRows(t, store,
		&Row{TsMs: crossesMidnight.UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", TotalTokens: 5},
		&Row{TsMs: sameDay.UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", TotalTokens: 7},
		&Row{TsMs: sameDay.UnixMilli(), APIKey: "sk-phone", Model: "gpt-5.6-sol", TotalTokens: 99},
	)

	result, errSummary := store.Summary(context.Background(), SummaryRequest{
		Filter: Filter{
			From:   crossesMidnight.Add(-time.Hour),
			To:     sameDay.Add(time.Hour),
			APIKey: []string{"sk-mac"},
		},
		GroupBy:  []string{GroupDay},
		Location: location,
		OrderBy:  GroupDay,
		Order:    "asc",
		Limit:    500,
	})
	if errSummary != nil {
		t.Fatalf("Summary: %v", errSummary)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 local days", len(result.Rows))
	}

	wantFirst := time.Date(2026, 9, 12, 0, 0, 0, 0, location).Format(time.RFC3339)
	wantSecond := time.Date(2026, 9, 13, 0, 0, 0, 0, location).Format(time.RFC3339)
	if result.Rows[0].Keys[GroupDay] != wantFirst {
		t.Fatalf("first bucket = %v, want %s", result.Rows[0].Keys[GroupDay], wantFirst)
	}
	if result.Rows[1].Keys[GroupDay] != wantSecond {
		t.Fatalf("second bucket = %v, want %s", result.Rows[1].Keys[GroupDay], wantSecond)
	}
	if result.Totals.Requests != 2 || result.Totals.TotalTokens != 12 {
		t.Fatalf("totals = %+v, want the sk-mac rows only", result.Totals)
	}
	if result.TZ != "America/New_York" {
		t.Fatalf("tz = %q, want America/New_York", result.TZ)
	}
}

func TestStoreRequestsCursorPagination(t *testing.T) {
	store := newTestStore(t, 0)
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		writeRows(t, store, &Row{
			TsMs:  base.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Model: "gpt-5.6-sol",
		})
	}

	filter := Filter{From: base.Add(-time.Hour), To: base.Add(time.Hour)}
	first, errFirst := store.Requests(context.Background(), filter, 2, nil, time.UTC)
	if errFirst != nil {
		t.Fatalf("Requests: %v", errFirst)
	}
	if len(first.Rows) != 2 {
		t.Fatalf("first page rows = %d, want 2", len(first.Rows))
	}
	if first.Rows[0].ID <= first.Rows[1].ID {
		t.Fatalf("rows not ordered newest first: %d then %d", first.Rows[0].ID, first.Rows[1].ID)
	}
	if first.Rows[0].Ts != base.Add(4*time.Minute).Format(time.RFC3339) {
		t.Fatalf("newest ts = %q", first.Rows[0].Ts)
	}
	if first.NextBefore == nil || *first.NextBefore != first.Rows[1].ID {
		t.Fatalf("next_before = %v, want %d", first.NextBefore, first.Rows[1].ID)
	}

	second, errSecond := store.Requests(context.Background(), filter, 2, first.NextBefore, time.UTC)
	if errSecond != nil {
		t.Fatalf("Requests page 2: %v", errSecond)
	}
	if len(second.Rows) != 2 {
		t.Fatalf("second page rows = %d, want 2", len(second.Rows))
	}
	if second.Rows[0].ID >= *first.NextBefore {
		t.Fatalf("second page did not advance past cursor %d", *first.NextBefore)
	}

	last, errLast := store.Requests(context.Background(), filter, 2, second.NextBefore, time.UTC)
	if errLast != nil {
		t.Fatalf("Requests page 3: %v", errLast)
	}
	if len(last.Rows) != 1 {
		t.Fatalf("third page rows = %d, want 1", len(last.Rows))
	}
	if last.NextBefore != nil {
		t.Fatalf("next_before = %v on the final page, want nil", last.NextBefore)
	}
}

func TestStorePruneOlderThanDeletesExpiredRows(t *testing.T) {
	store := newTestStore(t, 0)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	writeRows(t, store,
		&Row{TsMs: now.AddDate(0, 0, -10).UnixMilli(), Model: "old"},
		&Row{TsMs: now.AddDate(0, 0, -5).UnixMilli(), Model: "old"},
		&Row{TsMs: now.UnixMilli(), Model: "fresh"},
	)

	deleted, errPrune := store.PruneOlderThan(now.AddDate(0, 0, -7))
	if errPrune != nil {
		t.Fatalf("PruneOlderThan: %v", errPrune)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	count, errCount := store.Count(context.Background())
	if errCount != nil {
		t.Fatalf("Count: %v", errCount)
	}
	if count != 2 {
		t.Fatalf("remaining rows = %d, want 2", count)
	}
}

func TestStoreMetaReportsBoundsAndDimensions(t *testing.T) {
	store := newTestStore(t, 14)
	oldest := time.Now().Add(-2 * time.Hour)
	newest := time.Now().Add(-time.Minute)
	writeRows(t, store,
		&Row{TsMs: oldest.UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", Provider: "codex", AuthID: "codex-a.json"},
		&Row{TsMs: newest.UnixMilli(), APIKey: "sk-phone", Model: "claude-opus-5", Provider: "claude", AuthID: ""},
	)

	meta, errMeta := store.Meta(context.Background(), time.UTC)
	if errMeta != nil {
		t.Fatalf("Meta: %v", errMeta)
	}
	if !meta.Enabled || meta.RetentionDays != 14 {
		t.Fatalf("meta = %+v, want enabled with retention 14", meta)
	}
	if meta.Rows != 2 {
		t.Fatalf("rows = %d, want 2", meta.Rows)
	}
	if meta.Oldest == nil || meta.Newest == nil {
		t.Fatalf("oldest/newest = %v/%v, want both set", meta.Oldest, meta.Newest)
	}
	if meta.SizeBytes <= 0 {
		t.Fatalf("size_bytes = %d, want > 0", meta.SizeBytes)
	}
	if got := meta.Dimensions["api_key"]; len(got) != 2 || got[0] != "sk-mac" || got[1] != "sk-phone" {
		t.Fatalf("api_key dimension = %v", got)
	}
	if got := meta.Dimensions["auth_id"]; len(got) != 1 || got[0] != "codex-a.json" {
		t.Fatalf("auth_id dimension = %v, want the non-empty value only", got)
	}
}
