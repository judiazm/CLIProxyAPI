package util

import "testing"

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{name: "exact match", pattern: "gpt-5.1", value: "gpt-5.1", want: true},
		{name: "exact mismatch", pattern: "gpt-5.1", value: "gpt-5.2", want: false},
		{name: "empty pattern never matches", pattern: "", value: "gpt-5.1", want: false},
		{name: "prefix wildcard", pattern: "gpt-*", value: "gpt-6-astra", want: true},
		{name: "prefix wildcard mismatch", pattern: "gpt-*", value: "claude-sonnet-4-5", want: false},
		{name: "suffix wildcard", pattern: "*-astra", value: "gpt-6-astra", want: true},
		{name: "credential prefix wildcard", pattern: "natacha/*", value: "natacha/gpt-6-astra", want: true},
		{name: "credential prefix is not implicit", pattern: "gpt-*", value: "natacha/gpt-6-astra", want: false},
		{name: "middle segments in order", pattern: "gpt-*-astra*", value: "gpt-6-astra-preview", want: true},
		{name: "middle segment out of order", pattern: "*astra*gpt*", value: "gpt-6-astra", want: false},
		{name: "bare wildcard matches everything", pattern: "*", value: "anything", want: true},
		{name: "bare wildcard matches empty value", pattern: "*", value: "", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchWildcard(tc.pattern, tc.value); got != tc.want {
				t.Fatalf("MatchWildcard(%q, %q) = %t, want %t", tc.pattern, tc.value, got, tc.want)
			}
		})
	}
}
