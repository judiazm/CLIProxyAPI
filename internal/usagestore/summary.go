package usagestore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// p95MaxRows is the largest group for which latency p95 is computed. Larger groups report
// a null p95 rather than buffering every latency.
const p95MaxRows = 50000

// MaxGroupBy is the number of group columns a summary query accepts.
const MaxGroupBy = 3

// Group column names accepted by `group_by`.
const (
	GroupAPIKey          = "api_key"
	GroupModel           = "model"
	GroupAlias           = "alias"
	GroupAuthID          = "auth_id"
	GroupProvider        = "provider"
	GroupAuthType        = "auth_type"
	GroupEndpoint        = "endpoint"
	GroupSessionID       = "session_id"
	GroupReasoningEffort = "reasoning_effort"
	GroupServiceTier     = "service_tier"
	GroupStream          = "stream"
	GroupFailed          = "failed"
	GroupDay             = "day"
	GroupHour            = "hour"
	GroupWeek            = "week"
	GroupMonth           = "month"
)

// GroupColumns lists every accepted `group_by` value in documentation order.
var GroupColumns = []string{
	GroupAPIKey, GroupModel, GroupAlias, GroupAuthID, GroupProvider, GroupAuthType,
	GroupEndpoint, GroupSessionID, GroupReasoningEffort, GroupServiceTier,
	GroupStream, GroupFailed, GroupDay, GroupHour, GroupWeek, GroupMonth,
}

// IsGroupColumn reports whether name is an accepted `group_by` value.
func IsGroupColumn(name string) bool {
	for _, column := range GroupColumns {
		if column == name {
			return true
		}
	}
	return false
}

// IsTimeBucket reports whether name buckets by time rather than by a stored column.
func IsTimeBucket(name string) bool {
	switch name {
	case GroupDay, GroupHour, GroupWeek, GroupMonth:
		return true
	}
	return false
}

// Metric names accepted by `order_by`.
var metricNames = []string{
	"requests", "failed", "input_tokens", "cache_read_tokens", "cache_creation_tokens",
	"cached_tokens", "output_tokens", "reasoning_tokens", "total_tokens",
	"latency_ms_avg", "latency_ms_p95", "ttft_ms_avg", "first_at", "last_at",
}

// IsMetric reports whether name is an accepted `order_by` metric.
func IsMetric(name string) bool {
	for _, metric := range metricNames {
		if metric == name {
			return true
		}
	}
	return false
}

// SummaryMetrics holds the aggregated values of one group, or of the whole result set.
type SummaryMetrics struct {
	Requests            int64   `json:"requests"`
	Failed              int64   `json:"failed"`
	InputTokens         int64   `json:"input_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	LatencyMsAvg        float64 `json:"latency_ms_avg"`
	LatencyMsP95        *int64  `json:"latency_ms_p95"`
	TTFTMsAvg           float64 `json:"ttft_ms_avg"`
	FirstAt             *string `json:"first_at"`
	LastAt              *string `json:"last_at"`
}

// SummaryRow is one aggregated group.
type SummaryRow struct {
	Keys map[string]any `json:"keys"`
	SummaryMetrics
}

// SummaryRequest describes one aggregate query.
type SummaryRequest struct {
	Filter   Filter
	GroupBy  []string
	Location *time.Location
	OrderBy  string
	Order    string
	Limit    int
}

// SummaryResult is the response payload of the summary endpoint.
type SummaryResult struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	TZ      string         `json:"tz"`
	GroupBy []string       `json:"group_by"`
	Rows    []SummaryRow   `json:"rows"`
	Totals  SummaryMetrics `json:"totals"`
}

type aggregate struct {
	keys      map[string]any
	bucketMs  map[string]int64
	latencies []int64
	overflow  bool
	p95       *int64
	p95Done   bool

	requests            int64
	failed              int64
	inputTokens         int64
	cacheReadTokens     int64
	cacheCreationTokens int64
	cachedTokens        int64
	outputTokens        int64
	reasoningTokens     int64
	totalTokens         int64
	latencySum          int64
	ttftSum             int64
	firstMs             int64
	lastMs              int64
	seen                bool
}

// summaryScanRow is the subset of columns one summary query reads.
type summaryScanRow struct {
	tsMs                int64
	failed              int64
	latencyMs           int64
	ttftMs              int64
	inputTokens         int64
	outputTokens        int64
	reasoningTokens     int64
	cachedTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64
	totalTokens         int64
	groupValues         []string
}

// Summary aggregates the filtered rows into at most Limit groups plus a totals row.
func (s *Store) Summary(ctx context.Context, req SummaryRequest) (*SummaryResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usagestore: store unavailable")
	}
	loc := req.Location
	if loc == nil {
		loc = time.UTC
	}

	columnGroups := make([]string, 0, len(req.GroupBy))
	for _, group := range req.GroupBy {
		if !IsTimeBucket(group) {
			columnGroups = append(columnGroups, group)
		}
	}

	selectColumns := append([]string{
		"ts_ms", "failed", "latency_ms", "ttft_ms", "input_tokens", "output_tokens",
		"reasoning_tokens", "cached_tokens", "cache_read_tokens", "cache_creation_tokens",
		"total_tokens",
	}, columnGroups...)

	predicate, args := req.Filter.where()
	query := "SELECT " + strings.Join(selectColumns, ", ") + " FROM usage_requests WHERE " + predicate

	rows, errQuery := s.db.QueryContext(ctx, query, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestore: summary query: %w", errQuery)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.Errorf("usage store: close summary rows failed: %v", errClose)
		}
	}()

	groups := make(map[string]*aggregate)
	order := make([]*aggregate, 0, 64)
	totals := &aggregate{}

	scan := summaryScanRow{groupValues: make([]string, len(columnGroups))}
	dest := []any{
		&scan.tsMs, &scan.failed, &scan.latencyMs, &scan.ttftMs, &scan.inputTokens,
		&scan.outputTokens, &scan.reasoningTokens, &scan.cachedTokens, &scan.cacheReadTokens,
		&scan.cacheCreationTokens, &scan.totalTokens,
	}
	for i := range scan.groupValues {
		dest = append(dest, &scan.groupValues[i])
	}

	var keyBuilder strings.Builder
	for rows.Next() {
		if errScan := rows.Scan(dest...); errScan != nil {
			return nil, fmt.Errorf("usagestore: scan summary row: %w", errScan)
		}
		totals.add(&scan)

		keyBuilder.Reset()
		columnIndex := 0
		for _, group := range req.GroupBy {
			if IsTimeBucket(group) {
				bucket := bucketStart(time.UnixMilli(scan.tsMs), group, loc)
				writeKeyPart(&keyBuilder, strconv.FormatInt(bucket.UnixMilli(), 10))
				continue
			}
			writeKeyPart(&keyBuilder, scan.groupValues[columnIndex])
			columnIndex++
		}

		key := keyBuilder.String()
		group, exists := groups[key]
		if !exists {
			group = &aggregate{keys: make(map[string]any, len(req.GroupBy))}
			columnIndex = 0
			for _, name := range req.GroupBy {
				switch {
				case IsTimeBucket(name):
					bucket := bucketStart(time.UnixMilli(scan.tsMs), name, loc)
					group.keys[name] = bucket.Format(time.RFC3339)
					if group.bucketMs == nil {
						group.bucketMs = make(map[string]int64, 1)
					}
					group.bucketMs[name] = bucket.UnixMilli()
				case name == GroupStream || name == GroupFailed:
					group.keys[name] = scan.groupValues[columnIndex] == "1"
					columnIndex++
				default:
					group.keys[name] = scan.groupValues[columnIndex]
					columnIndex++
				}
			}
			groups[key] = group
			order = append(order, group)
		}
		group.add(&scan)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestore: iterate summary rows: %w", errRows)
	}

	sortAggregates(order, req.OrderBy, req.Order)

	limit := req.Limit
	if limit > 0 && len(order) > limit {
		order = order[:limit]
	}

	result := &SummaryResult{
		TZ:      loc.String(),
		GroupBy: req.GroupBy,
		Rows:    make([]SummaryRow, 0, len(order)),
		Totals:  totals.metrics(loc),
	}
	if result.GroupBy == nil {
		result.GroupBy = []string{}
	}
	if !req.Filter.From.IsZero() {
		result.From = req.Filter.From.In(loc).Format(time.RFC3339)
	}
	if !req.Filter.To.IsZero() {
		result.To = req.Filter.To.In(loc).Format(time.RFC3339)
	}
	for _, group := range order {
		result.Rows = append(result.Rows, SummaryRow{Keys: group.keys, SummaryMetrics: group.metrics(loc)})
	}
	return result, nil
}

// writeKeyPart appends a length-prefixed part so distinct value tuples never collide.
func writeKeyPart(builder *strings.Builder, value string) {
	builder.WriteString(strconv.Itoa(len(value)))
	builder.WriteByte(':')
	builder.WriteString(value)
}

func (a *aggregate) add(scan *summaryScanRow) {
	a.requests++
	if scan.failed != 0 {
		a.failed++
	}
	a.inputTokens += scan.inputTokens
	a.outputTokens += scan.outputTokens
	a.reasoningTokens += scan.reasoningTokens
	a.cachedTokens += scan.cachedTokens
	a.cacheReadTokens += scan.cacheReadTokens
	a.cacheCreationTokens += scan.cacheCreationTokens
	a.totalTokens += scan.totalTokens
	a.latencySum += scan.latencyMs
	a.ttftSum += scan.ttftMs

	if !a.seen || scan.tsMs < a.firstMs {
		a.firstMs = scan.tsMs
	}
	if !a.seen || scan.tsMs > a.lastMs {
		a.lastMs = scan.tsMs
	}
	a.seen = true

	if a.overflow {
		return
	}
	if len(a.latencies) >= p95MaxRows {
		a.overflow = true
		a.latencies = nil
		return
	}
	a.latencies = append(a.latencies, scan.latencyMs)
}

func (a *aggregate) metrics(loc *time.Location) SummaryMetrics {
	metrics := SummaryMetrics{
		Requests:            a.requests,
		Failed:              a.failed,
		InputTokens:         a.inputTokens,
		CacheReadTokens:     a.cacheReadTokens,
		CacheCreationTokens: a.cacheCreationTokens,
		CachedTokens:        a.cachedTokens,
		OutputTokens:        a.outputTokens,
		ReasoningTokens:     a.reasoningTokens,
		TotalTokens:         a.totalTokens,
	}
	if a.requests > 0 {
		metrics.LatencyMsAvg = round2(float64(a.latencySum) / float64(a.requests))
		metrics.TTFTMsAvg = round2(float64(a.ttftSum) / float64(a.requests))
	}
	metrics.LatencyMsP95 = a.latencyP95()
	if a.seen {
		first := time.UnixMilli(a.firstMs).In(loc).Format(time.RFC3339)
		last := time.UnixMilli(a.lastMs).In(loc).Format(time.RFC3339)
		metrics.FirstAt = &first
		metrics.LastAt = &last
	}
	return metrics
}

// percentile returns the nearest-rank percentile of values. It sorts values in place.
func percentile(values []int64, fraction float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	rank := int(math.Ceil(fraction*float64(len(values)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(values) {
		rank = len(values) - 1
	}
	return values[rank]
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

// bucketStart truncates t to the start of its day, hour, ISO week (Monday) or month in loc.
func bucketStart(t time.Time, kind string, loc *time.Location) time.Time {
	local := t.In(loc)
	year, month, day := local.Date()
	switch kind {
	case GroupHour:
		return time.Date(year, month, day, local.Hour(), 0, 0, 0, loc)
	case GroupWeek:
		offset := (int(local.Weekday()) + 6) % 7
		return time.Date(year, month, day, 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
	case GroupMonth:
		return time.Date(year, month, 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(year, month, day, 0, 0, 0, 0, loc)
	}
}

func sortAggregates(groups []*aggregate, orderBy, order string) {
	descending := !strings.EqualFold(order, "asc")
	less := aggregateLess(orderBy)
	sort.SliceStable(groups, func(i, j int) bool {
		if descending {
			return less(groups[j], groups[i])
		}
		return less(groups[i], groups[j])
	})
}

// aggregateLess returns the ascending comparison for the requested sort column.
func aggregateLess(orderBy string) func(a, b *aggregate) bool {
	if IsTimeBucket(orderBy) {
		return func(a, b *aggregate) bool { return a.bucketMs[orderBy] < b.bucketMs[orderBy] }
	}
	if IsGroupColumn(orderBy) {
		return func(a, b *aggregate) bool {
			return groupKeyString(a, orderBy) < groupKeyString(b, orderBy)
		}
	}
	switch orderBy {
	case "requests":
		return func(a, b *aggregate) bool { return a.requests < b.requests }
	case "failed":
		return func(a, b *aggregate) bool { return a.failed < b.failed }
	case "input_tokens":
		return func(a, b *aggregate) bool { return a.inputTokens < b.inputTokens }
	case "cache_read_tokens":
		return func(a, b *aggregate) bool { return a.cacheReadTokens < b.cacheReadTokens }
	case "cache_creation_tokens":
		return func(a, b *aggregate) bool { return a.cacheCreationTokens < b.cacheCreationTokens }
	case "cached_tokens":
		return func(a, b *aggregate) bool { return a.cachedTokens < b.cachedTokens }
	case "output_tokens":
		return func(a, b *aggregate) bool { return a.outputTokens < b.outputTokens }
	case "reasoning_tokens":
		return func(a, b *aggregate) bool { return a.reasoningTokens < b.reasoningTokens }
	case "latency_ms_avg":
		return func(a, b *aggregate) bool { return a.averageLatency() < b.averageLatency() }
	case "latency_ms_p95":
		return func(a, b *aggregate) bool { return a.p95Latency() < b.p95Latency() }
	case "ttft_ms_avg":
		return func(a, b *aggregate) bool { return a.averageTTFT() < b.averageTTFT() }
	case "first_at":
		return func(a, b *aggregate) bool { return a.firstMs < b.firstMs }
	case "last_at":
		return func(a, b *aggregate) bool { return a.lastMs < b.lastMs }
	default:
		return func(a, b *aggregate) bool { return a.totalTokens < b.totalTokens }
	}
}

func groupKeyString(a *aggregate, column string) string {
	if a == nil || a.keys == nil {
		return ""
	}
	switch value := a.keys[column].(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	default:
		return fmt.Sprint(value)
	}
}

func (a *aggregate) averageLatency() float64 {
	if a.requests == 0 {
		return 0
	}
	return float64(a.latencySum) / float64(a.requests)
}

func (a *aggregate) averageTTFT() float64 {
	if a.requests == 0 {
		return 0
	}
	return float64(a.ttftSum) / float64(a.requests)
}

// latencyP95 computes the group's latency p95 once and caches it. Groups larger than
// p95MaxRows report nil.
func (a *aggregate) latencyP95() *int64 {
	if a.p95Done {
		return a.p95
	}
	a.p95Done = true
	if a.overflow || len(a.latencies) == 0 {
		a.p95 = nil
		return nil
	}
	value := percentile(a.latencies, 0.95)
	a.p95 = &value
	return a.p95
}

func (a *aggregate) p95Latency() int64 {
	value := a.latencyP95()
	if value == nil {
		return -1
	}
	return *value
}
