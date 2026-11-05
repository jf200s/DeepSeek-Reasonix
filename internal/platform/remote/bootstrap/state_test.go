package bootstrap

import (
	"strings"
	"testing"
	"time"
)

func TestServeStateRoundTrip(t *testing.T) {
	st := ServeState{
		PID:                1234,
		Addr:               "127.0.0.1:38121",
		Workspace:          "/home/dev/app",
		Version:            "1.9.0",
		TokenFile:          "/home/dev/.reasonix/remote/serve-x.token",
		LogFile:            "/home/dev/.reasonix/remote/serve-x.log",
		StartedAt:          1_700_000_000,
		IdleTimeoutSeconds: 300,
	}
	data, err := MarshalState(st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalState(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != st {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, st)
	}
}

// TestServeStateBackwardCompat: an older record missing the newer optional
// fields must decode without error, leaving zero values.
func TestServeStateBackwardCompat(t *testing.T) {
	old := `{"pid": 42, "addr": "127.0.0.1:9000", "workspace": "/app", "token_file": "/t"}`
	st, err := UnmarshalState([]byte(old))
	if err != nil {
		t.Fatalf("old record failed to decode: %v", err)
	}
	if st.PID != 42 || st.Addr != "127.0.0.1:9000" || st.Version != "" || st.StartedAt != 0 || st.IdleTimeoutSeconds != 0 {
		t.Fatalf("backward-compat decode wrong: %+v", st)
	}
}

// The idle timeout is optional: an unset one must not appear in the record.
func TestServeStateIdleTimeoutOmitEmpty(t *testing.T) {
	data, err := MarshalState(ServeState{
		PID: 42, Addr: "127.0.0.1:9000", Workspace: "/app", TokenFile: "/t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "idle_timeout_seconds") {
		t.Fatalf("zero idle timeout was recorded: %s", data)
	}
}

// The record says which idle policy the launch used: -1 for "until stopped",
// 0 for "as soon as no client is left", and whole seconds otherwise. A record
// that reports never as 0 would read as the most aggressive policy there is.
func TestIdleTimeoutSecondsRecordsNeverZeroAndTheDefault(t *testing.T) {
	ninety := 90 * time.Second
	never := -time.Minute
	zero := time.Duration(0)
	quarter := 250 * time.Millisecond
	cases := []struct {
		name string
		in   *time.Duration
		want int
	}{
		{"unset takes the default", nil, 600},
		{"never", &never, -1},
		{"zero", &zero, 0},
		{"positive", &ninety, 90},
		{"rounds up", &quarter, 1},
	}
	for _, tc := range cases {
		if got := idleTimeoutSeconds(tc.in); got != tc.want {
			t.Errorf("%s: idleTimeoutSeconds = %d, want %d", tc.name, got, tc.want)
		}
	}

	// -1 survives the JSON record; the kernel is still described as "until
	// stopped" after a reconnect reads it back.
	data, err := MarshalState(ServeState{
		PID: 42, Addr: "127.0.0.1:9000", Workspace: "/app", TokenFile: "/t",
		IdleTimeoutSeconds: idleTimeoutSeconds(&never),
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := UnmarshalState(data)
	if err != nil || st.IdleTimeoutSeconds != -1 {
		t.Fatalf("round trip = %d, %v; want -1", st.IdleTimeoutSeconds, err)
	}
}

func TestPathsFor(t *testing.T) {
	paths := pathsFor("/home/dev", "/home/dev/projects/app")
	if paths.Dir != "/home/dev/.reasonix/remote" {
		t.Fatalf("Dir = %q", paths.Dir)
	}
	if !strings.HasPrefix(paths.StateJSON, paths.Dir+"/serve-") || !strings.HasSuffix(paths.StateJSON, ".json") {
		t.Fatalf("StateJSON = %q", paths.StateJSON)
	}
	if !strings.HasSuffix(paths.TokenFile, ".token") || !strings.HasSuffix(paths.PortFile, ".port") {
		t.Fatalf("token/port names wrong: %+v", paths)
	}
}
