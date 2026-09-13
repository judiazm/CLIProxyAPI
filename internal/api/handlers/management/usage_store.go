package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestore"
)

const (
	summaryDefaultLimit  = 500
	summaryMaxLimit      = 5000
	requestsDefaultLimit = 200
	requestsMaxLimit     = 2000
	defaultWindow        = 24 * time.Hour
)

// usageStoreQuery holds the parameters shared by the usage-store endpoints.
type usageStoreQuery struct {
	filter   usagestore.Filter
	location *time.Location
}

// GetUsageStoreSummary aggregates persisted usage records into grouped totals.
func (h *Handler) GetUsageStoreSummary(c *gin.Context) {
	store := usagestore.Current()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage store disabled"})
		return
	}

	parsed, errParse := parseUsageStoreQuery(c)
	if errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParse.Error()})
		return
	}

	groupBy, errGroupBy := parseUsageStoreGroupBy(c.QueryArray("group_by"))
	if errGroupBy != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errGroupBy.Error()})
		return
	}

	orderBy := strings.TrimSpace(c.Query("order_by"))
	if orderBy == "" {
		orderBy = "total_tokens"
	}
	if !usagestore.IsMetric(orderBy) && !usagestore.IsGroupColumn(orderBy) {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown order_by %q", orderBy)})
		return
	}

	order := strings.ToLower(strings.TrimSpace(c.Query("order")))
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order must be asc or desc"})
		return
	}

	limit, errLimit := parseUsageStoreLimit(c.Query("limit"), summaryDefaultLimit, summaryMaxLimit)
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errLimit.Error()})
		return
	}

	result, errSummary := store.Summary(c.Request.Context(), usagestore.SummaryRequest{
		Filter:   parsed.filter,
		GroupBy:  groupBy,
		Location: parsed.location,
		OrderBy:  orderBy,
		Order:    order,
		Limit:    limit,
	})
	if errSummary != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errSummary.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetUsageStoreRequests returns the newest persisted usage records, newest first.
func (h *Handler) GetUsageStoreRequests(c *gin.Context) {
	store := usagestore.Current()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage store disabled"})
		return
	}

	parsed, errParse := parseUsageStoreQuery(c)
	if errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParse.Error()})
		return
	}

	limit, errLimit := parseUsageStoreLimit(c.Query("limit"), requestsDefaultLimit, requestsMaxLimit)
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errLimit.Error()})
		return
	}

	var before *int64
	if raw := strings.TrimSpace(c.Query("before")); raw != "" {
		value, errParseBefore := strconv.ParseInt(raw, 10, 64)
		if errParseBefore != nil || value < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before must be a non-negative row id"})
			return
		}
		before = &value
	}

	result, errRequests := store.Requests(c.Request.Context(), parsed.filter, limit, before, parsed.location)
	if errRequests != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errRequests.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetUsageStoreMeta reports store status, row bounds and the distinct dimension values.
func (h *Handler) GetUsageStoreMeta(c *gin.Context) {
	store := usagestore.Current()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage store disabled"})
		return
	}

	location, errLocation := parseUsageStoreLocation(c.Query("tz"))
	if errLocation != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errLocation.Error()})
		return
	}

	meta, errMeta := store.Meta(c.Request.Context(), location)
	if errMeta != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errMeta.Error()})
		return
	}
	c.JSON(http.StatusOK, meta)
}

// parseUsageStoreQuery reads the time range, timezone and repeatable filters.
func parseUsageStoreQuery(c *gin.Context) (usageStoreQuery, error) {
	location, errLocation := parseUsageStoreLocation(c.Query("tz"))
	if errLocation != nil {
		return usageStoreQuery{}, errLocation
	}

	to := time.Now()
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		parsed, errParse := parseUsageStoreTime(raw)
		if errParse != nil {
			return usageStoreQuery{}, fmt.Errorf("invalid to: %w", errParse)
		}
		to = parsed
	}
	from := to.Add(-defaultWindow)
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		parsed, errParse := parseUsageStoreTime(raw)
		if errParse != nil {
			return usageStoreQuery{}, fmt.Errorf("invalid from: %w", errParse)
		}
		from = parsed
	}

	filter := usagestore.Filter{From: from, To: to}
	for _, column := range usagestore.FilterColumns {
		values := c.QueryArray(column)
		if len(values) == 0 {
			continue
		}
		filter.SetFilterColumn(column, values)
	}

	failed, errFailed := parseUsageStoreBool(c, "failed")
	if errFailed != nil {
		return usageStoreQuery{}, errFailed
	}
	filter.Failed = failed

	stream, errStream := parseUsageStoreBool(c, "stream")
	if errStream != nil {
		return usageStoreQuery{}, errStream
	}
	filter.Stream = stream

	return usageStoreQuery{filter: filter, location: location}, nil
}

func parseUsageStoreLocation(raw string) (*time.Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.UTC, nil
	}
	location, errLoad := time.LoadLocation(raw)
	if errLoad != nil {
		return nil, fmt.Errorf("unknown tz %q", raw)
	}
	return location, nil
}

// parseUsageStoreTime accepts an RFC3339 timestamp or unix milliseconds.
func parseUsageStoreTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return parsed, nil
	}
	millis, errParse := strconv.ParseInt(raw, 10, 64)
	if errParse != nil {
		return time.Time{}, fmt.Errorf("expected RFC3339 or unix milliseconds, got %q", raw)
	}
	return time.UnixMilli(millis), nil
}

func parseUsageStoreBool(c *gin.Context, name string) (*bool, error) {
	values := c.QueryArray(name)
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 1 {
		return nil, fmt.Errorf("%s accepts a single value", name)
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "true":
		value := true
		return &value, nil
	case "false":
		value := false
		return &value, nil
	default:
		return nil, fmt.Errorf("%s must be true or false", name)
	}
}

// parseUsageStoreLimit parses limit, applying the default when absent and clamping to max.
func parseUsageStoreLimit(raw string, defaultLimit, maxLimit int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLimit, nil
	}
	limit, errParse := strconv.Atoi(raw)
	if errParse != nil || limit <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit, nil
}

// parseUsageStoreGroupBy splits and validates the group_by parameter. Each occurrence may
// itself be a comma-separated list.
func parseUsageStoreGroupBy(values []string) ([]string, error) {
	groups := make([]string, 0, usagestore.MaxGroupBy)
	seen := make(map[string]struct{}, usagestore.MaxGroupBy)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !usagestore.IsGroupColumn(part) {
				return nil, fmt.Errorf("unknown group_by %q", part)
			}
			if _, duplicate := seen[part]; duplicate {
				return nil, fmt.Errorf("duplicate group_by %q", part)
			}
			seen[part] = struct{}{}
			groups = append(groups, part)
		}
	}
	if len(groups) > usagestore.MaxGroupBy {
		return nil, fmt.Errorf("group_by accepts at most %d columns", usagestore.MaxGroupBy)
	}
	return groups, nil
}
