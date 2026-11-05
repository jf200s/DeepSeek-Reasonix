package cli

import (
	"context"
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

// idleHost records what the serve loop armed; the rest of the host interface is
// embedded empty because nothing here calls it.
type idleHost struct {
	serveHost
	calls  int
	window time.Duration
	cancel context.CancelFunc
}

func (h *idleHost) SetIdleTimeout(window time.Duration, cancel context.CancelFunc) {
	h.calls++
	h.window = window
	h.cancel = cancel
}

// The flag reaches the hub exactly when it names a real window: -1 is "never"
// and must leave the setter untouched, while 0 and positive durations arm the
// hub with the same window and a cancel that drains the serve loop.
func TestIdleTimeoutFlagArmsTheHubOnlyForARealWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flag   time.Duration
		armed  bool
		window time.Duration
	}{
		{"never", -1, false, 0},
		{"zero", 0, true, 0},
		{"positive", 15 * time.Minute, true, 15 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleExitFromFlag(tc.flag); tc.armed != (got != nil) {
				t.Fatalf("idleExitFromFlag(%v) = %v, armed want %v", tc.flag, got, tc.armed)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			host := &idleHost{}
			armIdleExit(host, idleExitFromFlag(tc.flag), cancel)
			if !tc.armed {
				if host.calls != 0 {
					t.Fatalf("the -1 sentinel armed the hub %d time(s)", host.calls)
				}
				return
			}
			if host.calls != 1 {
				t.Fatalf("setter called %d time(s), want 1", host.calls)
			}
			if host.window != tc.window {
				t.Fatalf("armed window = %v, want %v", host.window, tc.window)
			}
			if host.cancel == nil {
				t.Fatal("the hub was armed without a cancel")
			}
			host.cancel()
			if ctx.Err() == nil {
				t.Fatal("the armed cancel did not cancel the serve loop's context")
			}
		})
	}
}
