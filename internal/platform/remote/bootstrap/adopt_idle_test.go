package bootstrap

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
	"reasonix/internal/platform/remote"
)

// A record that another client wrote while this connect was waiting for the
// serve lock is that serve's real record. Its idle policy has to be compared
// like any other — including the missing field, which no validated request
// asks for — instead of being overwritten with this connect's request and
// reused. The write must also leave the found record alone.
func TestTryReuseComparesAnAdoptedRecordInsteadOfRewritingIt(t *testing.T) {
	skipOnWindows(t)
	for _, tc := range []struct {
		name     string
		recorded int
		request  int
		want     bool
	}{
		{"another policy", -1, 600, false},
		{"missing field", 0, 600, false},
		{"the same policy", 600, 600, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testenv.TempDir(t)
			paths := pathsFor(root, root)
			if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for path, body := range map[string]string{
				paths.PortFile:  "127.0.0.1:47001\n",
				paths.PidFile:   "4242\n",
				paths.TokenFile: "external-token\n",
			} {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// The record appears after tryReuse's first readState and before
			// adoptPublished re-reads it under the serve lock: one client's
			// launch finishing while another is blocking on the lock.
			var injected atomic.Bool
			conn := newFakeConn(t, root, func(cmd string) (remote.ExecResult, error) {
				switch {
				case strings.Contains(cmd, "kill -0 4242"):
					if injected.CompareAndSwap(false, true) {
						data, _ := MarshalState(ServeState{
							PID: 4242, Addr: "127.0.0.1:47001", Workspace: root,
							TokenFile: paths.TokenFile, IdleTimeoutSeconds: tc.recorded,
						})
						_ = os.WriteFile(paths.StateJSON, data, 0o600)
					}
					return ok("1\n")
				case strings.Contains(cmd, "uname"):
					return ok("Linux x86_64\n")
				}
				return ok("")
			})

			_, _, reused, err := tryReuse(context.Background(), conn, posixShell{}, conn.fs, paths, "", Broker{}, tc.request, false, time.Now, root)
			if err != nil {
				t.Fatalf("tryReuse: %v", err)
			}
			if reused != tc.want {
				t.Fatalf("reused = %v, want %v for a record with idle %d and a request of %d", reused, tc.want, tc.recorded, tc.request)
			}
			rec, err := readStateFile(paths.StateJSON)
			if err != nil || rec.PID != 4242 || rec.IdleTimeoutSeconds != tc.recorded {
				t.Fatalf("the adopted record changed: %+v, %v; want pid 4242 idle %d", rec, err, tc.recorded)
			}
		})
	}
}

// A serve found only through its published files has no record of its own, so
// this connect's request becomes the record — and is already there, with the
// lock still held, when the adoption returns.
func TestAdoptingAPublishedServeRecordsTheRequestedIdlePolicy(t *testing.T) {
	skipOnWindows(t)
	root := testenv.TempDir(t)
	paths := pathsFor(root, root)
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		paths.PortFile:  "127.0.0.1:47001\n",
		paths.PidFile:   "4242\n",
		paths.TokenFile: "external-token\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conn := newFakeConn(t, root, func(cmd string) (remote.ExecResult, error) {
		switch {
		case strings.Contains(cmd, "kill -0 4242"):
			return ok("1\n")
		case strings.Contains(cmd, "--version"):
			return ok("reasonix v2.31.0\n")
		}
		return ok("")
	})

	_, _, reused, err := tryReuse(context.Background(), conn, posixShell{}, conn.fs, paths, "", Broker{}, 600, false, time.Now, root)
	if err != nil || !reused {
		t.Fatalf("reused = %v, err = %v; want the published serve adopted", reused, err)
	}
	rec, err := readStateFile(paths.StateJSON)
	if err != nil || rec.IdleTimeoutSeconds != 600 {
		t.Fatalf("adopted record = %+v, %v; want the requested idle 600", rec, err)
	}
}
