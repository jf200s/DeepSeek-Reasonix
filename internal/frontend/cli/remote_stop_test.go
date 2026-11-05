package cli

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/contract/config"
	"reasonix/internal/platform/remote"
	"reasonix/internal/platform/remote/bootstrap"
	"reasonix/internal/platform/remote/sftpfs"
)

// stopFiles is the distribution's file layer for a stop: it serves one recorded
// serve state, or reports that there is none.
type stopFiles struct {
	state   []byte
	removed bool
}

func (f *stopFiles) Stat(context.Context, string) (sftpfs.Entry, error) {
	return sftpfs.Entry{}, nil
}

func (f *stopFiles) ReadFile(context.Context, string, int64) ([]byte, bool, sftpfs.Kind, error) {
	if f.state == nil {
		return nil, false, sftpfs.KindText, errors.New("no such file")
	}
	return f.state, false, sftpfs.KindText, nil
}

func (f *stopFiles) WriteFileAtomic(context.Context, string, []byte, fs.FileMode) error { return nil }
func (f *stopFiles) MkdirAll(context.Context, string) error                             { return nil }
func (f *stopFiles) MkdirExclusive(context.Context, string) error                       { return nil }
func (f *stopFiles) Chmod(context.Context, string, fs.FileMode) error                   { return nil }
func (f *stopFiles) Remove(context.Context, string, bool) error {
	f.removed = true
	return nil
}
func (f *stopFiles) RealPath(context.Context, string) (string, error) { return "/home/dev", nil }

// stopConn is a fake WSL distribution: enough of one for bootstrap.Status and
// bootstrap.Stop, recording the command that signals the kernel.
type stopConn struct {
	mu     sync.Mutex
	files  *stopFiles
	execs  []string
	mapped []string
}

func (c *stopConn) Exec(_ context.Context, cmd string) (remote.ExecResult, error) {
	c.mu.Lock()
	c.execs = append(c.execs, cmd)
	c.mu.Unlock()
	switch {
	case strings.Contains(cmd, "uname"):
		return remote.ExecResult{Stdout: []byte("Linux x86_64\n")}, nil
	case strings.Contains(cmd, "ps -p"):
		return remote.ExecResult{Stdout: []byte("1\n")}, nil
	}
	return remote.ExecResult{}, nil
}

func (c *stopConn) Files() (sftpfs.Files, error) { return c.files, nil }

func (c *stopConn) ToLinux(_ context.Context, p string) (string, error) {
	c.mu.Lock()
	c.mapped = append(c.mapped, p)
	c.mu.Unlock()
	return "/mnt/c/work/app", nil
}

func (c *stopConn) ranContaining(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cmd := range c.execs {
		if strings.Contains(cmd, sub) {
			return true
		}
	}
	return false
}

func useStopConn(t *testing.T, entry config.RemoteHostEntry, files *stopFiles) *stopConn {
	t.Helper()
	conn := &stopConn{files: files}
	previous := buildWSLStopConn
	t.Cleanup(func() { buildWSLStopConn = previous })
	buildWSLStopConn = func(context.Context, config.RemoteHostEntry) (wslStopConn, error) {
		return conn, nil
	}
	if err := editUserConfig(func(c *config.Config) error { return c.UpsertRemoteHost(entry) }); err != nil {
		t.Fatalf("write host: %v", err)
	}
	return conn
}

func wslStopEntry(workspace string) config.RemoteHostEntry {
	return config.RemoteHostEntry{Name: "dev", Kind: config.RemoteKindWSL, Distro: "Ubuntu", Workspace: workspace}
}

func recordedState(t *testing.T, workspace string, pid int) []byte {
	t.Helper()
	data, err := bootstrap.MarshalState(bootstrap.ServeState{PID: pid, Addr: "127.0.0.1:4242", Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRemoteStopOnWSLNeedsNoAnchorAndSignalsTheRecordedKernel(t *testing.T) {
	isolateCLIConfigHome(t)
	conn := useStopConn(t, wslStopEntry("/home/dev/app"), &stopFiles{state: recordedState(t, "/home/dev/app", 4242)})

	stdout, stderr := captureCLIOutput(t, func() {
		if code := remoteCommand([]string{"stop", "dev"}, "test"); code != 0 {
			t.Errorf("remote stop = %d, want 0", code)
		}
	})
	if stdout != "kernel stopped (pid 4242)\n" {
		t.Fatalf("stdout = %q, want the recorded pid", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !conn.ranContaining("kill -TERM 4242") {
		t.Fatalf("the kernel was not signalled; ran %v", conn.execs)
	}
	if !conn.files.removed {
		t.Fatal("the serve state was left behind")
	}
	if conn.ranContaining("/bin/sh") {
		t.Fatal("a stop opened an anchor session")
	}
}

func TestRemoteStopOnWSLSaysSoWhenNoKernelIsRunning(t *testing.T) {
	isolateCLIConfigHome(t)
	conn := useStopConn(t, wslStopEntry("/home/dev/app"), &stopFiles{})

	stdout, stderr := captureCLIOutput(t, func() {
		if code := remoteCommand([]string{"stop", "dev"}, "test"); code != 0 {
			t.Errorf("remote stop = %d, want 0", code)
		}
	})
	if stdout != "no kernel running\n" {
		t.Fatalf("stdout = %q, want no kernel running", stdout)
	}
	if strings.TrimSpace(stderr) != "" || conn.ranContaining("kill -") {
		t.Fatalf("stderr = %q, execs = %v", stderr, conn.execs)
	}
}

// A configured Windows folder is recorded by the kernel as the distribution
// spells it; the stop has to translate it the same way an attach does.
func TestRemoteStopOnWSLTranslatesAWindowsWorkspace(t *testing.T) {
	isolateCLIConfigHome(t)
	conn := useStopConn(t, wslStopEntry(`C:\work\app`), &stopFiles{state: recordedState(t, "/mnt/c/work/app", 77)})

	stdout, _ := captureCLIOutput(t, func() {
		if code := remoteCommand([]string{"stop", "dev"}, "test"); code != 0 {
			t.Errorf("remote stop = %d, want 0", code)
		}
	})
	if stdout != "kernel stopped (pid 77)\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(conn.mapped) == 0 || conn.mapped[0] != `C:\work\app` {
		t.Fatalf("workspace was not translated: %v", conn.mapped)
	}
}

func TestRemoteStopReportsEachConfiguredWorkspace(t *testing.T) {
	isolateCLIConfigHome(t)
	entry := wslStopEntry("/home/dev/app")
	entry.Workspaces = []string{"/home/dev/other"}
	useStopConn(t, entry, &stopFiles{state: recordedState(t, "/home/dev/app", 4242)})

	stdout, _ := captureCLIOutput(t, func() {
		if code := remoteCommand([]string{"stop", "dev"}, "test"); code != 0 {
			t.Errorf("remote stop = %d, want 0", code)
		}
	})
	for _, want := range []string{"/home/dev/app: kernel stopped (pid 4242)", "/home/dev/other: no kernel running"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
	}
}

// A recorded pid that no longer belongs to our serve is a stale record. Stop
// clears the state, but the report must not claim a process it never signalled.
func TestRemoteStopSaysNoKernelForAStaleRecord(t *testing.T) {
	entry := config.RemoteHostEntry{Name: "box", Host: "h", Workspace: "/srv/a"}
	stdout, _ := captureCLIOutput(t, func() {
		if code := remoteStopKernels(context.Background(), entry,
			func(string) (bootstrap.ServeState, bool, error) {
				return bootstrap.ServeState{PID: 4242, Workspace: "/srv/a"}, false, nil
			},
			func(string) error { return nil }); code != 0 {
			t.Errorf("code = %d, want 0", code)
		}
	})
	if stdout != "no kernel running\n" {
		t.Fatalf("stdout = %q, want no kernel running", stdout)
	}
}

// The SSH branch runs the same per-workspace stop over bootstrap.Status/Stop.
// Those are what `remote serve stop` already uses; this pins the reporting
// around them without a server.
func TestRemoteStopKernelsStopsEveryWorkspaceAndReportsTheResults(t *testing.T) {
	entry := config.RemoteHostEntry{Name: "box", Host: "h", Workspace: "/srv/a", Workspaces: []string{"/srv/b"}}
	var stopped []string
	code := 0
	captureCLIOutput(t, func() {
		code = remoteStopKernels(context.Background(), entry,
			func(ws string) (bootstrap.ServeState, bool, error) {
				return bootstrap.ServeState{PID: len(ws), Workspace: ws}, true, nil
			},
			func(ws string) error {
				stopped = append(stopped, ws)
				return nil
			})
	})
	if code != 0 || strings.Join(stopped, ",") != "/srv/a,/srv/b" {
		t.Fatalf("code = %d, stopped = %v", code, stopped)
	}
}

func TestRemoteStopFailsWhenAKernelCannotBeStopped(t *testing.T) {
	entry := config.RemoteHostEntry{Name: "box", Host: "h", Workspace: "/srv/a"}
	stdout, stderr := captureCLIOutput(t, func() {
		if code := remoteStopKernels(context.Background(), entry,
			func(string) (bootstrap.ServeState, bool, error) { return bootstrap.ServeState{PID: 1}, true, nil },
			func(string) error { return errors.New("permission denied") }); code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
	})
	if stdout != "" || !strings.Contains(stderr, "permission denied") {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

// An empty workspace means the login home, which is where a bare connect put
// the kernel; it must still be asked about once.
func TestRemoteStopAsksAboutTheHomeWhenNoWorkspaceIsConfigured(t *testing.T) {
	entry := config.RemoteHostEntry{Name: "box", Host: "h"}
	var asked []string
	code := 0
	captureCLIOutput(t, func() {
		code = remoteStopKernels(context.Background(), entry,
			func(ws string) (bootstrap.ServeState, bool, error) {
				asked = append(asked, ws)
				return bootstrap.ServeState{}, false, nil
			},
			func(string) error { return nil })
	})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(asked) != 1 || asked[0] != "" {
		t.Fatalf("asked about %v, want one empty (home) workspace", asked)
	}
}

// `remote add --idle-timeout` writes the per-host override, and leaving the flag
// out leaves the row unset rather than on the flag's zero. An unset SSH host
// keeps its long-standing stay-resident behaviour (never idle-exits), while an
// unset WSL distribution takes the 10-minute reconnect window.
func TestRemoteAddIdleTimeoutFlag(t *testing.T) {
	isolateCLIConfigHome(t)
	if code := remoteAddCLI([]string{"box", "dev@10.0.0.9", "--idle-timeout", "5"}); code != 0 {
		t.Fatalf("add with --idle-timeout = %d", code)
	}
	if code := remoteAddCLI([]string{"plain", "dev@10.0.0.10"}); code != 0 {
		t.Fatalf("add without --idle-timeout = %d", code)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	box, _ := cfg.RemoteHost("box")
	if box.IdleTimeoutMinutes == nil || *box.IdleTimeoutMinutes != 5 {
		t.Fatalf("box idle timeout = %v, want an explicit 5", box.IdleTimeoutMinutes)
	}
	if box.IdleTimeout(nil) != 5*time.Minute {
		t.Fatalf("box IdleTimeout = %v, want 5m", box.IdleTimeout(nil))
	}
	plain, _ := cfg.RemoteHost("plain")
	if plain.IdleTimeoutMinutes != nil || plain.IdleTimeout(nil) >= 0 {
		t.Fatalf("plain idle timeout = %v / %v, want never (an unconfigured SSH host stays resident)", plain.IdleTimeoutMinutes, plain.IdleTimeout(nil))
	}

	// Adding the host again replaces the row whole, so an absent flag must mean
	// "leave the override alone" rather than "reset it to the default".
	if code := remoteAddCLI([]string{"box", "dev@10.0.0.9"}); code != 0 {
		t.Fatalf("re-add without --idle-timeout = %d", code)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	box, _ = cfg.RemoteHost("box")
	if box.IdleTimeoutMinutes == nil || *box.IdleTimeoutMinutes != 5 {
		t.Fatalf("re-adding dropped the idle timeout: %v", box.IdleTimeoutMinutes)
	}

	// The WSL form has the same flag and the same preservation rule.
	if code := remoteWSLAddCLI([]string{"dev", "Ubuntu", "--idle-timeout", "-1"}); code != 0 {
		t.Fatalf("wsl add with --idle-timeout = %d", code)
	}
	if code := remoteWSLAddCLI([]string{"wsl-default", "Ubuntu"}); code != 0 {
		t.Fatalf("wsl add without --idle-timeout = %d", code)
	}
	if code := remoteWSLAddCLI([]string{"dev", "Ubuntu"}); code != 0 {
		t.Fatalf("wsl re-add without --idle-timeout = %d", code)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := cfg.RemoteHost("dev")
	if dev.IdleTimeoutMinutes == nil || *dev.IdleTimeoutMinutes != -1 {
		t.Fatalf("dev idle timeout = %v, want and keep an explicit -1", dev.IdleTimeoutMinutes)
	}
	if dev.IdleTimeout(nil) >= 0 {
		t.Fatalf("dev IdleTimeout = %v, want never", dev.IdleTimeout(nil))
	}
	def, _ := cfg.RemoteHost("wsl-default")
	if def.IdleTimeoutMinutes != nil || def.IdleTimeout(nil) != 10*time.Minute {
		t.Fatalf("wsl-default idle timeout = %v / %v, want the 10-minute default", def.IdleTimeoutMinutes, def.IdleTimeout(nil))
	}
}
