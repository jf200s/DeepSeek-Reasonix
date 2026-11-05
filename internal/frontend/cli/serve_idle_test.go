package cli

import (
	"testing"
	"time"
)

func TestParseIdleTimeout(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{in: "", want: -1},
		{in: "-1", want: -1},
		{in: "0", want: 0},
		{in: "150ms", want: 150 * time.Millisecond},
		{in: "15m", want: 15 * time.Minute},
		{in: "-2", bad: true},
		{in: "-1s", bad: true},
		{in: "soon", bad: true},
	}
	for _, tc := range cases {
		got, err := parseIdleTimeout(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("parseIdleTimeout(%q) = %v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseIdleTimeout(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseIdleTimeout(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
