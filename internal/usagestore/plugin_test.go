package usagestore

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPluginWritesRecordVisibleInSummary(t *testing.T) {
	store := newTestStore(t, 0)
	restore := SetCurrentForTest(store)
	t.Cleanup(restore)

	at := time.Date(2026, 9, 12, 9, 15, 0, 0, time.UTC)
	plugin := &storePlugin{}
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider:     "codex",
		ExecutorType: "codex",
		Model:        "gpt-5.6-sol",
		APIKey:       "sk-mac",
		AuthID:       "codex-a.json",
		AuthIndex:    "0f1e2d3c4b5a6978",
		AuthType:     "oauth",
		Stream:       true,
		RequestedAt:  at,
		Latency:      1500 * time.Millisecond,
		TTFT:         250 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     100,
			OutputTokens:    20,
			CacheReadTokens: 40,
			TotalTokens:     120,
		},
	})
	if errFlush := store.Flush(context.Background()); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}

	result, errSummary := store.Summary(context.Background(), SummaryRequest{
		Filter:   Filter{From: at.Add(-time.Hour), To: at.Add(time.Hour)},
		GroupBy:  []string{GroupAPIKey, GroupModel},
		Location: time.UTC,
		OrderBy:  "total_tokens",
		Order:    "desc",
		Limit:    500,
	})
	if errSummary != nil {
		t.Fatalf("Summary: %v", errSummary)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(result.Rows))
	}

	row := result.Rows[0]
	if row.Keys[GroupAPIKey] != "sk-mac" || row.Keys[GroupModel] != "gpt-5.6-sol" {
		t.Fatalf("keys = %v", row.Keys)
	}
	if row.Requests != 1 || row.Failed != 0 {
		t.Fatalf("requests/failed = %d/%d, want 1/0", row.Requests, row.Failed)
	}
	if row.InputTokens != 100 || row.OutputTokens != 20 || row.CacheReadTokens != 40 || row.TotalTokens != 120 {
		t.Fatalf("tokens = %+v", row.SummaryMetrics)
	}
	if row.LatencyMsAvg != 1500 || row.TTFTMsAvg != 250 {
		t.Fatalf("latency/ttft avg = %v/%v, want 1500/250", row.LatencyMsAvg, row.TTFTMsAvg)
	}

	// The alias falls back to the model, and the auth identifiers round-trip unchanged.
	requests, errRequests := store.Requests(context.Background(), Filter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, 10, nil, time.UTC)
	if errRequests != nil {
		t.Fatalf("Requests: %v", errRequests)
	}
	if len(requests.Rows) != 1 {
		t.Fatalf("request rows = %d, want 1", len(requests.Rows))
	}
	stored := requests.Rows[0]
	if stored.Alias != "gpt-5.6-sol" || stored.AuthID != "codex-a.json" || stored.AuthIndex != "0f1e2d3c4b5a6978" {
		t.Fatalf("stored row = %+v", stored)
	}
	if !stored.Stream || !stored.Generate || stored.Failed || stored.StatusCode != 0 {
		t.Fatalf("stored flags = stream %v generate %v failed %v status %d", stored.Stream, stored.Generate, stored.Failed, stored.StatusCode)
	}
}

func TestPluginRecordsFailureStatus(t *testing.T) {
	store := newTestStore(t, 0)
	restore := SetCurrentForTest(store)
	t.Cleanup(restore)

	at := time.Date(2026, 9, 12, 9, 15, 0, 0, time.UTC)
	(&storePlugin{}).HandleUsage(context.Background(), coreusage.Record{
		Model:       "gpt-5.6-sol",
		APIKey:      "sk-mac",
		RequestedAt: at,
		Failed:      true,
		Fail:        coreusage.Failure{StatusCode: 429, Body: "rate limited"},
	})
	if errFlush := store.Flush(context.Background()); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}

	requests, errRequests := store.Requests(context.Background(), Filter{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, 10, nil, time.UTC)
	if errRequests != nil {
		t.Fatalf("Requests: %v", errRequests)
	}
	if len(requests.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(requests.Rows))
	}
	if !requests.Rows[0].Failed || requests.Rows[0].StatusCode != 429 {
		t.Fatalf("failed/status = %v/%d, want true/429", requests.Rows[0].Failed, requests.Rows[0].StatusCode)
	}
}

func TestPluginIsNoOpWhileStoreDisabled(t *testing.T) {
	restore := SetCurrentForTest(nil)
	t.Cleanup(restore)
	// Must not panic and must not require a store.
	(&storePlugin{}).HandleUsage(context.Background(), coreusage.Record{Model: "gpt-5.6-sol"})
}
