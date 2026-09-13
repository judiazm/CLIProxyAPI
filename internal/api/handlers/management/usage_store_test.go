package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestore"
)

// newManagementUsageStore opens a temp-file store, installs it as the active store and
// restores the previous one when the test ends.
func newManagementUsageStore(t *testing.T, rows ...*usagestore.Row) *usagestore.Store {
	t.Helper()
	store, errOpen := usagestore.Open(usagestore.Options{
		Path: filepath.Join(t.TempDir(), "usage.db"),
	})
	if errOpen != nil {
		t.Fatalf("Open: %v", errOpen)
	}
	restore := usagestore.SetCurrentForTest(store)
	t.Cleanup(func() {
		restore()
		if errClose := store.Close(); errClose != nil {
			t.Errorf("Close: %v", errClose)
		}
	})
	for _, row := range rows {
		store.Enqueue(row)
	}
	if errFlush := store.Flush(context.Background()); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}
	return store
}

func performUsageStoreRequest(t *testing.T, handle func(*Handler, *gin.Context), target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	handle(&Handler{}, ginCtx)
	return rec
}

func TestGetUsageStoreSummaryGroupsByAPIKeyAndModel(t *testing.T) {
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	newManagementUsageStore(t,
		&usagestore.Row{TsMs: base.UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", LatencyMs: 100, TotalTokens: 12},
		&usagestore.Row{TsMs: base.Add(time.Minute).UnixMilli(), APIKey: "sk-mac", Model: "gpt-5.6-sol", LatencyMs: 300, TotalTokens: 24},
		&usagestore.Row{TsMs: base.Add(2 * time.Minute).UnixMilli(), APIKey: "sk-phone", Model: "claude-opus-5", TotalTokens: 2},
	)

	target := "/v0/management/usage-store/summary?group_by=api_key,model&from=" +
		base.Add(-time.Hour).Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339)
	rec := performUsageStoreRequest(t, (*Handler).GetUsageStoreSummary, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		TZ      string   `json:"tz"`
		GroupBy []string `json:"group_by"`
		Rows    []struct {
			Keys         map[string]any `json:"keys"`
			Requests     int64          `json:"requests"`
			TotalTokens  int64          `json:"total_tokens"`
			LatencyMsP95 *int64         `json:"latency_ms_p95"`
		} `json:"rows"`
		Totals struct {
			Requests    int64 `json:"requests"`
			TotalTokens int64 `json:"total_tokens"`
		} `json:"totals"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.TZ != "UTC" {
		t.Fatalf("tz = %q, want UTC", payload.TZ)
	}
	if len(payload.GroupBy) != 2 || payload.GroupBy[0] != "api_key" || payload.GroupBy[1] != "model" {
		t.Fatalf("group_by = %v", payload.GroupBy)
	}
	if len(payload.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(payload.Rows))
	}
	if payload.Rows[0].Keys["api_key"] != "sk-mac" || payload.Rows[0].TotalTokens != 36 {
		t.Fatalf("top row = %+v", payload.Rows[0])
	}
	if payload.Rows[0].LatencyMsP95 == nil || *payload.Rows[0].LatencyMsP95 != 300 {
		t.Fatalf("top row p95 = %v, want 300", payload.Rows[0].LatencyMsP95)
	}
	if payload.Totals.Requests != 3 || payload.Totals.TotalTokens != 38 {
		t.Fatalf("totals = %+v", payload.Totals)
	}
}

func TestGetUsageStoreSummaryRejectsBadParameters(t *testing.T) {
	newManagementUsageStore(t)

	cases := []struct {
		name   string
		target string
	}{
		{"unknown group", "/v0/management/usage-store/summary?group_by=nope"},
		{"too many groups", "/v0/management/usage-store/summary?group_by=api_key,model,alias,provider"},
		{"duplicate group", "/v0/management/usage-store/summary?group_by=api_key,api_key"},
		{"unknown order_by", "/v0/management/usage-store/summary?order_by=nope"},
		{"bad order", "/v0/management/usage-store/summary?order=sideways"},
		{"bad limit", "/v0/management/usage-store/summary?limit=0"},
		{"bad tz", "/v0/management/usage-store/summary?tz=Mars/Olympus"},
		{"bad from", "/v0/management/usage-store/summary?from=yesterday"},
		{"bad failed", "/v0/management/usage-store/summary?failed=maybe"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rec := performUsageStoreRequest(t, (*Handler).GetUsageStoreSummary, testCase.target)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
			}
			var payload map[string]string
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatalf("unmarshal response: %v", errUnmarshal)
			}
			if payload["error"] == "" {
				t.Fatalf("missing error field in %s", rec.Body.String())
			}
		})
	}
}

func TestGetUsageStoreRequestsPagesWithCursor(t *testing.T) {
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	rows := make([]*usagestore.Row, 0, 3)
	for i := 0; i < 3; i++ {
		rows = append(rows, &usagestore.Row{
			TsMs:  base.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Model: "gpt-5.6-sol",
		})
	}
	newManagementUsageStore(t, rows...)

	window := "&from=" + base.Add(-time.Hour).Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339)
	rec := performUsageStoreRequest(t, (*Handler).GetUsageStoreRequests,
		"/v0/management/usage-store/requests?limit=2"+window)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var page struct {
		Rows []struct {
			ID    int64  `json:"id"`
			Ts    string `json:"ts"`
			Model string `json:"model"`
		} `json:"rows"`
		NextBefore *int64 `json:"next_before"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &page); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if len(page.Rows) != 2 || page.NextBefore == nil {
		t.Fatalf("first page = %+v", page)
	}
	if page.Rows[0].Ts != base.Add(2*time.Minute).Format(time.RFC3339) {
		t.Fatalf("newest ts = %q", page.Rows[0].Ts)
	}

	rec = performUsageStoreRequest(t, (*Handler).GetUsageStoreRequests,
		"/v0/management/usage-store/requests?limit=2"+window+"&before="+strconv.FormatInt(*page.NextBefore, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var second struct {
		Rows       []struct{ ID int64 } `json:"rows"`
		NextBefore *int64               `json:"next_before"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &second); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if len(second.Rows) != 1 {
		t.Fatalf("second page rows = %d, want 1", len(second.Rows))
	}
	if second.NextBefore != nil {
		t.Fatalf("next_before = %v on the final page, want null", second.NextBefore)
	}
}

func TestGetUsageStoreMetaReportsDimensions(t *testing.T) {
	newManagementUsageStore(t, &usagestore.Row{
		TsMs:   time.Now().Add(-time.Minute).UnixMilli(),
		APIKey: "sk-mac", Model: "gpt-5.6-sol", Provider: "codex", AuthID: "codex-a.json",
	})

	rec := performUsageStoreRequest(t, (*Handler).GetUsageStoreMeta, "/v0/management/usage-store/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var meta struct {
		Enabled    bool                `json:"enabled"`
		Path       string              `json:"path"`
		Rows       int64               `json:"rows"`
		Dimensions map[string][]string `json:"dimensions"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &meta); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if !meta.Enabled || meta.Rows != 1 || meta.Path == "" {
		t.Fatalf("meta = %+v", meta)
	}
	if got := meta.Dimensions["model"]; len(got) != 1 || got[0] != "gpt-5.6-sol" {
		t.Fatalf("model dimension = %v", got)
	}
}

func TestUsageStoreEndpointsReportDisabled(t *testing.T) {
	restore := usagestore.SetCurrentForTest(nil)
	t.Cleanup(restore)

	handlers := map[string]func(*Handler, *gin.Context){
		"/v0/management/usage-store/summary":  (*Handler).GetUsageStoreSummary,
		"/v0/management/usage-store/requests": (*Handler).GetUsageStoreRequests,
		"/v0/management/usage-store/meta":     (*Handler).GetUsageStoreMeta,
	}
	for target, handle := range handlers {
		rec := performUsageStoreRequest(t, handle, target)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503 body=%s", target, rec.Code, rec.Body.String())
		}
		var payload map[string]string
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if payload["error"] != "usage store disabled" {
			t.Fatalf("%s error = %q, want \"usage store disabled\"", target, payload["error"])
		}
	}
}
