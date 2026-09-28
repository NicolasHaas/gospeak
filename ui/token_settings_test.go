package ui

import "testing"

func TestParseTokenSettings(t *testing.T) {
	for _, tc := range []struct {
		uses, days  string
		wantUses    int
		wantSeconds int64
		valid       bool
	}{
		{"10", "1", 10, 86400, true},
		{"0", "0", 0, 0, true},
		{"1", "3650", 1, 315360000, true},
		{"oops", "1", 0, 0, false},
		{"-1", "1", 0, 0, false},
		{"2147483648", "1", 0, 0, false},
		{"1", "bad", 0, 0, false},
		{"1", "-1", 0, 0, false},
		{"1", "3651", 0, 0, false},
	} {
		uses, seconds, err := parseTokenSettings(tc.uses, tc.days)
		if (err == nil) != tc.valid || (tc.valid && (uses != tc.wantUses || seconds != tc.wantSeconds)) {
			t.Errorf("parseTokenSettings(%q, %q) = %d, %d, %v", tc.uses, tc.days, uses, seconds, err)
		}
	}
}
