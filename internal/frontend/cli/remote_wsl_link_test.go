package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/contract/config"
	"reasonix/internal/platform/remote"
	"reasonix/internal/platform/remote/attach"
	"reasonix/internal/platform/remote/sftpfs"
)

func TestRemoteWSLAddWritesAWSLMachineAndRefusesWhatWSLWouldReadAsAnOption(t *testing.T) {
	isolateCLIConfigHome(t)
	if code := remoteWSLCLI([]string{"add", "dev", "Ubuntu-24.04", "--workspace", "/home/me/app"}); code != 0 {
		t.Fatalf("add = %d", code)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := cfg.RemoteHost("dev")
	if !ok || e.HostKind() != config.RemoteKindWSL || e.Distro != "Ubuntu-24.04" || e.Workspace != "/home/me/app" || e.Host != "" || e.Target() != "wsl:Ubuntu-24.04" {
		t.Fatalf("saved = %+v, %v", e, ok)
	}
	for _, bad := range []string{"-d", "--exec", "a b", `a"b`, strings.Repeat("a", 65)} {
		if code := remoteWSLCLI([]string{"add", "x", bad}); code == 0 {
			t.Errorf("distro %q was accepted", bad)
		}
	}
	if code := remoteAddCLI([]string{"box", "ada@10.0.0.4"}); code != 0 {
		t.Fatalf("ssh add = %d", code)
	}
	if code := remoteWSLCLI([]string{"add", "box", "Ubuntu"}); code == 0 {
		t.Fatal("a WSL entry replaced an SSH machine of the same name")
	}
	if cfg, _ = config.Load(); func() bool {
		b, _ := cfg.RemoteHost("box")
		return b.HostKind() != config.RemoteKindSSH || b.Host != "10.0.0.4"
	}() {
		t.Fatal("the refused add changed the SSH machine")
	}
}

type failingLink struct{ started bool }

func (l *failingLink) Exec(context.Context, string) (remote.ExecResult, error) {
	return remote.ExecResult{}, errors.New("no exec")
}
func (l *failingLink) Files() (sftpfs.Files, error) { return nil, errors.New("no files") }
func (l *failingLink) Start(context.Context) error {
	l.started = true
	return errors.New("distribution would not start")
}
func (l *failingLink) Close() error                              { return nil }
func (l *failingLink) Subscribe(func(remote.StatusEvent)) func() { return func() {} }
func (l *failingLink) Forwards() attach.Forwards                 { return nil }
func (l *failingLink) Dirs(context.Context, string) (string, []sftpfs.Entry, error) {
	return "", nil, errors.New("no dirs")
}

// A WSL machine in the book is reached through the pool's WSL link and never
// through an SSH dial, from both test and connect.
func TestTestingAndConnectingAWSLMachineNeverDialsSSH(t *testing.T) {
	isolateCLIConfigHome(t)
	if code := remoteWSLCLI([]string{"add", "dev", "Ubuntu"}); code != 0 {
		t.Fatal(code)
	}
	link := &failingLink{}
	old := newWSLPool
	defer func() { newWSLPool = old }()
	newWSLPool = func(ctx context.Context, _, _ string) *attach.Pool {
		return attach.NewPool(ctx, attach.Options{
			Dial: func(string, attach.Prompts) (*remote.Client, error) {
				t.Error("an SSH dial was made for a WSL machine")
				return nil, errors.New("no")
			},
			DialWSL: func(config.RemoteHostEntry) (attach.Link, error) { return link, nil },
		})
	}
	if code := remoteTestCLI([]string{"dev"}, "test"); code != 1 || !link.started {
		t.Fatalf("remote test = %d, started = %v", code, link.started)
	}
	link.started = false
	if code := remoteConnectCLI([]string{"connect", "dev"}, "test"); code != 1 || !link.started {
		t.Fatalf("remote connect = %d, started = %v", code, link.started)
	}
}

// The SSH-shaped read commands have no WSL answer: status probes an SSH
// connection and forward ls lists rules that only an SSH dial applies. A host
// in the book under kind=wsl must be refused instead of printed as an empty
// SSH entry.
func TestWSLHostIsRefusedBySSHOnlyRemoteCommands(t *testing.T) {
	isolateCLIConfigHome(t)
	if code := remoteWSLCLI([]string{"add", "dev", "Ubuntu"}); code != 0 {
		t.Fatalf("wsl add = %d", code)
	}
	for name, run := range map[string]func() int{
		"status":     func() int { return remoteStatusCLI([]string{"dev"}) },
		"forward ls": func() int { return remoteForwardCLI([]string{"ls", "dev"}) },
	} {
		var code int
		stderr := captureStderr(t, func() { code = run() })
		if code != 1 {
			t.Fatalf("%s = %d, want 1", name, code)
		}
		if !strings.Contains(stderr, remote.ErrNotSSHHost.Error()) {
			t.Fatalf("%s stderr = %q, want %q", name, stderr, remote.ErrNotSSHHost)
		}
	}
}

// The SSH client's flags describe a hop, persisted forwards and a tunnel a WSL
// distribution does not have. Accepting them silently would promise work the
// connect never does.
func TestWSLConnectRefusesSSHOnlyFlags(t *testing.T) {
	isolateCLIConfigHome(t)
	if code := remoteWSLCLI([]string{"add", "dev", "Ubuntu"}); code != 0 {
		t.Fatalf("wsl add = %d", code)
	}
	for _, args := range [][]string{
		{"connect", "dev", "--no-serve"},
		{"open", "dev", "--no-serve"},
		{"connect", "dev", "--forward-only"},
		{"connect", "dev", "--local-port", "7777"},
		{"connect", "--local-port=0", "dev"},
	} {
		var code int
		stderr := captureStderr(t, func() { code = remoteConnectCLI(args, "test") })
		if code != 1 {
			t.Fatalf("%v = %d, want 1; stderr=%q", args, code, stderr)
		}
		if !strings.Contains(stderr, "only apply to ssh hosts") {
			t.Fatalf("%v stderr = %q, want the ssh-only refusal", args, stderr)
		}
	}
}

// Zero is not a writable idle timeout: -1 is how a host says "never" and a
// positive value is a real window. Both add forms refuse it with the range.
func TestAddingAHostRefusesZeroIdleTimeout(t *testing.T) {
	isolateCLIConfigHome(t)
	for name, run := range map[string]func() int{
		"ssh add": func() int { return remoteAddCLI([]string{"box", "dev@10.0.0.9", "--idle-timeout", "0"}) },
		"wsl add": func() int { return remoteWSLCLI([]string{"add", "dev", "Ubuntu", "--idle-timeout", "0"}) },
	} {
		var code int
		stderr := captureStderr(t, func() { code = run() })
		if code != 1 {
			t.Fatalf("%s with --idle-timeout 0 = %d, want 1", name, code)
		}
		if !strings.Contains(stderr, "-1") || !strings.Contains(stderr, "1..1440") {
			t.Fatalf("%s stderr = %q, want the writable range named", name, stderr)
		}
	}
}
