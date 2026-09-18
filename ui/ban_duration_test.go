package ui

import (
	"strings"
	"testing"
)

func TestBanDurationSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		maximum    bool
		want       int64
		wantErrSub string
	}{
		{name: "default one hour", input: "1h", want: 3600},
		{name: "editable minutes", input: "90m", want: 5400},
		{name: "fractional hours resolving to seconds", input: "1.5h", want: 5400},
		{name: "maximum checkbox overrides input", input: "not a duration", maximum: true, want: 10 * 365 * 24 * 60 * 60},
		{name: "exact maximum", input: "87600h", want: 10 * 365 * 24 * 60 * 60},
		{name: "empty", wantErrSub: "enter a duration"},
		{name: "invalid", input: "one hour", wantErrSub: "use a duration"},
		{name: "zero", input: "0s", wantErrSub: "greater than zero"},
		{name: "negative", input: "-1h", wantErrSub: "greater than zero"},
		{name: "sub-second", input: "1.5s", wantErrSub: "whole seconds"},
		{name: "above maximum", input: "87600h1s", wantErrSub: "10 years"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := banDurationSeconds(tc.input, tc.maximum)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("banDurationSeconds(%q, %t) error = %v, want substring %q", tc.input, tc.maximum, err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("banDurationSeconds(%q, %t): %v", tc.input, tc.maximum, err)
			}
			if got != tc.want {
				t.Fatalf("banDurationSeconds(%q, %t) = %d, want %d", tc.input, tc.maximum, got, tc.want)
			}
		})
	}
}
