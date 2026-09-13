package usagestore

import (
	"strings"
	"time"
)

// Filter narrows the rows a query considers. Values within one slice are ORed, and the
// slices are ANDed with each other and with the time range.
type Filter struct {
	From time.Time
	To   time.Time

	APIKey          []string
	Model           []string
	Alias           []string
	AuthID          []string
	Provider        []string
	AuthType        []string
	SessionID       []string
	ReasoningEffort []string
	Endpoint        []string

	Failed *bool
	Stream *bool
}

// where renders the filter as a SQL predicate plus its bind arguments.
func (f Filter) where() (string, []any) {
	clauses := make([]string, 0, 12)
	args := make([]any, 0, 16)

	if !f.From.IsZero() {
		clauses = append(clauses, "ts_ms >= ?")
		args = append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		clauses = append(clauses, "ts_ms < ?")
		args = append(args, f.To.UnixMilli())
	}

	appendIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for i, value := range values {
			placeholders[i] = "?"
			args = append(args, value)
		}
		clauses = append(clauses, column+" IN ("+strings.Join(placeholders, ", ")+")")
	}

	appendIn("api_key", f.APIKey)
	appendIn("model", f.Model)
	appendIn("alias", f.Alias)
	appendIn("auth_id", f.AuthID)
	appendIn("provider", f.Provider)
	appendIn("auth_type", f.AuthType)
	appendIn("session_id", f.SessionID)
	appendIn("reasoning_effort", f.ReasoningEffort)
	appendIn("endpoint", f.Endpoint)

	if f.Failed != nil {
		clauses = append(clauses, "failed = ?")
		args = append(args, boolToInt(*f.Failed))
	}
	if f.Stream != nil {
		clauses = append(clauses, "stream = ?")
		args = append(args, boolToInt(*f.Stream))
	}

	if len(clauses) == 0 {
		return "1 = 1", args
	}
	return strings.Join(clauses, " AND "), args
}

// FilterColumns are the repeatable filter query parameters, mapped to their SQL columns.
var FilterColumns = []string{
	"api_key", "model", "alias", "auth_id", "provider",
	"auth_type", "session_id", "reasoning_effort", "endpoint",
}

// SetFilterColumn assigns a repeatable filter by its query-parameter name.
// It reports whether the name is a known filter.
func (f *Filter) SetFilterColumn(name string, values []string) bool {
	if f == nil || len(values) == 0 {
		return isFilterColumn(name)
	}
	switch name {
	case "api_key":
		f.APIKey = values
	case "model":
		f.Model = values
	case "alias":
		f.Alias = values
	case "auth_id":
		f.AuthID = values
	case "provider":
		f.Provider = values
	case "auth_type":
		f.AuthType = values
	case "session_id":
		f.SessionID = values
	case "reasoning_effort":
		f.ReasoningEffort = values
	case "endpoint":
		f.Endpoint = values
	default:
		return false
	}
	return true
}

func isFilterColumn(name string) bool {
	for _, column := range FilterColumns {
		if column == name {
			return true
		}
	}
	return false
}
