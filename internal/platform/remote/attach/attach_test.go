package attach

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
	"reasonix/internal/platform/remote"
	"reasonix/internal/platform/remote/bootstrap"
	"reasonix/internal/platform/remote/sftpfs"
	"reasonix/internal/platform/remote/sshtest"
)

var portFileArg = regexp.MustCompile(`--port-file '([^']*)'`)

var psPID = regexp.MustCompile(`ps -p (\d+)`)

// machine is a host to attach to: a real SSH server with SFTP and forwarding,
// and HTTP servers standing in for the serves the bootstrap thinks it started.
// Each launch gets its own server, so closing one models a kernel that stopped
// answering while its link stayed up.
type machine struct {
	home    string
	kernels []*httptest.Server
	dial    func(host string, prompts Prompts) (*remote.Client, error)

	mu       sync.Mutex
	launched int
	pid      int
	// locateFlags overrides the remote's launch-flag answers; empty means all supported.
	locateFlags string
}

func fakeMachine(t *testing.T) *machine {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the bootstrap harness models a POSIX remote; exercised on Linux/macOS")
	}
	m := &machine{home: testenv.TempDir(t)}
	for range 4 {
		k := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "remote kernel: %s", r.URL.Path)
		}))
		m.kernels = append(m.kernels, k)
		t.Cleanup(k.Close)
	}

	srv := sshtest.Start(t, sshtest.Options{
		Password: "pw",
		SFTPRoot: m.home,
		Exec: func(cmd string) (string, string, int) {
			switch {
			case strings.Contains(cmd, "uname"):
				return "Linux x86_64\n", "", 0
			case strings.Contains(cmd, "command -v reasonix"):
				// LocateCommand's three lines: a path, a fresh version, and the
				// --port-file flag the launch needs.
				m.mu.Lock()
				flags := m.locateFlags
				m.mu.Unlock()
				if flags == "" {
					flags = kernelFlagsYes()
				}
				return "bin /usr/bin/reasonix\nver reasonix v9.9.0\n" + flags, "", 0
			case strings.Contains(cmd, "nohup"):
				// Stand in for serve coming up: publish the address of the HTTP
				// server the forward will actually reach.
				m.mu.Lock()
				m.launched++
				m.pid = 54320 + m.launched
				kernel := m.kernels[m.launched-1]
				pid := m.pid
				m.mu.Unlock()
				if hit := portFileArg.FindStringSubmatch(cmd); hit != nil {
					_ = os.WriteFile(hit[1], []byte(kernel.Listener.Addr().String()+"\n"), 0o600)
				}
				return fmt.Sprintf("%d\n", pid), "", 0
			case strings.Contains(cmd, "ps -p"):
				hit := psPID.FindStringSubmatch(cmd)
				m.mu.Lock()
				alive := hit != nil && hit[1] == fmt.Sprint(m.pid)
				m.mu.Unlock()
				if alive {
					return "1\n", "", 0
				}
				return "0\n", "", 0
			}
			return "", "", 0
		},
	})

	known := testenv.TempDir(t)
	m.dial = func(string, Prompts) (*remote.Client, error) {
		host, err := remote.ResolveHost(nil, "test@"+srv.Addr, nil)
		if err != nil {
			return nil, err
		}
		return remote.New(remote.Options{
			Host: host,
			Auth: remote.AuthOptions{Password: func() (string, error) { return "pw", nil }},
			HostKeys: &remote.HostKeyPolicy{
				SystemKnownHosts: []string{filepath.Join(known, "absent")},
				ManagedPath:      filepath.Join(known, "managed"),
				Prompt:           func(context.Context, remote.HostKeyQuestion) (bool, error) { return true, nil },
			},
		})
	}
	return m
}

func (m *machine) launches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.launched
}

// kill ends the current kernel without touching the link: the SSH connection
// outlives the serve, so the next request is what discovers the kernel is gone.
func (m *machine) kill() {
	m.mu.Lock()
	kernel := m.kernels[m.launched-1]
	m.pid = 0
	m.mu.Unlock()
	kernel.Close()
}

// withoutIdleFlag makes the remote kernel look like one that predates
// --idle-timeout, so a launch requiring it cannot use that binary.
func (m *machine) withoutIdleFlag() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locateFlags = strings.ReplaceAll(kernelFlagsYes(), "flag idle-timeout yes", "flag idle-timeout no")
}

// writeRemoteHostConfig records a host book entry so the pool reads a policy
// for "box" from configuration, the way a real host would have one.
func writeRemoteHostConfig(t *testing.T, home string, idleMinutes int) {
	t.Helper()
	writeRemoteHostPolicy(t, home, idleMinutes, "auto")
}

func writeRemoteHostPolicy(t *testing.T, home string, idleMinutes int, install string) {
	t.Helper()
	body := fmt.Sprintf("[remote]\n[[remote.hosts]]\nname = \"box\"\nhost = \"10.0.0.1\"\nidle_timeout_minutes = %d\nserve_install = %q\n", idleMinutes, install)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testPool(t *testing.T, m *machine) *Pool {
	t.Helper()
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := NewPool(ctx, Options{Dial: m.dial})
	t.Cleanup(p.Close)
	return p
}

func (m *machine) workspace(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(m.home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustAttach(t *testing.T, p *Pool, host, workspace string) *Endpoint {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ep, err := p.Attach(ctx, host, workspace, Call{})
	if err != nil {
		t.Fatalf("attach %s: %v", workspace, err)
	}
	return ep
}

// The whole point of the layer: what comes back is an address this machine can
// call, and what answers is the kernel on the other side of the link.
func TestAttachReachesTheRemoteKernelOverTheForward(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	ep := mustAttach(t, p, "box", m.workspace(t, "proj"))
	defer ep.Release()

	if ep.Token == "" {
		t.Fatal("no token came back, so nothing could authenticate to the remote gate")
	}
	resp, err := http.Get("http://" + ep.Addr + "/status")
	if err != nil {
		t.Fatalf("call the forwarded kernel: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "remote kernel: /status" {
		t.Fatalf("answer = %q, want the remote kernel's", got)
	}
}

// Two workspaces on one machine are one login, not two. A connection per
// workspace would re-prompt for the passphrase on the second folder opened.
func TestWorkspacesOnOneHostShareItsConnection(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	first := mustAttach(t, p, "box", m.workspace(t, "one"))
	defer first.Release()
	second := mustAttach(t, p, "box", m.workspace(t, "two"))
	defer second.Release()

	p.mu.Lock()
	links := len(p.links)
	spaces := len(p.links["box"].spaces)
	p.mu.Unlock()
	if links != 1 {
		t.Fatalf("pool holds %d connections for one host, want 1", links)
	}
	if spaces != 2 {
		t.Fatalf("connection carries %d workspaces, want 2", spaces)
	}
	if first.Addr == second.Addr {
		t.Fatal("both workspaces answered on one forward: they are separate kernels")
	}
}

// A second pane on a folder rides the forward the first one bound. Binding a
// second would start a second serve for one workspace on the far side.
func TestSecondPaneOnAWorkspaceReusesItsForward(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	ws := m.workspace(t, "proj")
	first := mustAttach(t, p, "box", ws)
	defer first.Release()
	second := mustAttach(t, p, "box", ws)
	defer second.Release()

	if first.Addr != second.Addr {
		t.Fatalf("second pane bound its own forward: %s vs %s", first.Addr, second.Addr)
	}
	if n := m.launches(); n != 1 {
		t.Fatalf("the remote was launched %d times for one workspace, want 1", n)
	}
}

// The last holder out closes the link. Releasing one pane while another is
// still open must not, which is the failure that makes the second pane go dark.
func TestTheLastReleaseClosesTheConnection(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	first := mustAttach(t, p, "box", m.workspace(t, "one"))
	second := mustAttach(t, p, "box", m.workspace(t, "two"))

	first.Release()
	p.mu.Lock()
	held := len(p.links)
	p.mu.Unlock()
	if held != 1 {
		t.Fatalf("releasing one of two panes dropped the connection (%d links)", held)
	}
	resp, err := http.Get("http://" + second.Addr + "/status")
	if err != nil {
		t.Fatalf("the surviving pane lost its forward: %v", err)
	}
	resp.Body.Close()

	second.Release()
	p.mu.Lock()
	held = len(p.links)
	p.mu.Unlock()
	if held != 0 {
		t.Fatalf("the last release left %d connections open", held)
	}
}

// A pane can be closed twice — by the user and by a shutdown sweep. The second
// close must not give back a reference the first one already returned.
func TestReleaseIsIdempotent(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	shared := mustAttach(t, p, "box", m.workspace(t, "one"))
	doomed := mustAttach(t, p, "box", m.workspace(t, "two"))
	defer shared.Release()

	doomed.Release()
	doomed.Release()

	p.mu.Lock()
	refs := p.links["box"].refs
	p.mu.Unlock()
	if refs != 1 {
		t.Fatalf("connection holds %d references after a double release, want 1", refs)
	}
}

// A refused login must not poison the host. Caching the failure would answer
// every later attempt with it, and the fix — type the password again — could
// never be reached without restarting the app.
func TestAFailedDialIsNotCached(t *testing.T) {
	m := fakeMachine(t)
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	ctx := t.Context()

	refuse := true
	p := NewPool(ctx, Options{Dial: func(host string, prompts Prompts) (*remote.Client, error) {
		if refuse {
			return nil, fmt.Errorf("authentication failed")
		}
		return m.dial(host, prompts)
	}})
	defer p.Close()

	if _, err := p.Attach(ctx, "box", m.workspace(t, "proj"), Call{}); err == nil {
		t.Fatal("a refused dial reported success")
	}
	p.mu.Lock()
	held := len(p.links)
	p.mu.Unlock()
	if held != 0 {
		t.Fatalf("the failed host stayed in the table (%d links)", held)
	}

	refuse = false
	ep := mustAttach(t, p, "box", m.workspace(t, "proj"))
	defer ep.Release()
}

// Resolve reports where a workspace answers now. A pane holds the endpoint it
// was opened on, and a kernel relaunched behind it lands on another port; this
// is the one call that follows it.
func TestResolveReportsTheForwardAWorkspaceIsOn(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	ws := m.workspace(t, "proj")
	ep := mustAttach(t, p, "box", ws)
	defer ep.Release()

	got, ok := p.Resolve("box", ws)
	if !ok || got.Addr != ep.Addr || got.Token != ep.Token {
		t.Fatalf("Resolve = %s/%s, %v, want the attached forward %s", got.Addr, got.Token, ok, ep.Addr)
	}
	if _, ok := p.Resolve("box", m.workspace(t, "other")); ok {
		t.Fatal("Resolve answered for a workspace the pool does not hold")
	}
}

// relaunchWatch is a Link whose far side refuses the bootstrap at its first
// step. Nothing needs to succeed: counting that step says whether a reconnect
// tried to launch the workspace again.
type relaunchWatch struct {
	mu    sync.Mutex
	files int
}

func (w *relaunchWatch) Exec(context.Context, string) (remote.ExecResult, error) {
	return remote.ExecResult{}, fmt.Errorf("no kernel here")
}

func (w *relaunchWatch) Files() (sftpfs.Files, error) {
	w.mu.Lock()
	w.files++
	w.mu.Unlock()
	return nil, fmt.Errorf("no files here")
}

func (w *relaunchWatch) Start(context.Context) error { return nil }
func (w *relaunchWatch) Close() error                { return nil }

func (w *relaunchWatch) Subscribe(func(remote.StatusEvent)) func() { return func() {} }
func (w *relaunchWatch) Forwards() Forwards                        { return nil }

func (w *relaunchWatch) Dirs(context.Context, string) (string, []sftpfs.Entry, error) {
	return "", nil, fmt.Errorf("no dirs here")
}

func (w *relaunchWatch) launches() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.files
}

// ephemeralWatch is relaunchWatch carrying the marker a link whose kernels may
// need relaunching on reconnect has, the way a WSL distribution does.
type ephemeralWatch struct{ *relaunchWatch }

func (ephemeralWatch) relaunchAfterReconnect() {}

func watchPool(t *testing.T, client Link) (*Pool, *link, *space) {
	t.Helper()
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	p := NewPool(t.Context(), Options{})
	t.Cleanup(p.Close)
	l, _ := p.holdLink("dev")
	l.client = client
	s, _ := p.holdSpace(l, "/srv/app")
	return p, l, s
}

// A WSL kernel ends with the session holding it, so a reconnect launches every
// workspace the link still carries again. Without this the pane keeps asking a
// kernel that is gone for the rest of the link's life.
func TestAReconnectLaunchesEphemeralKernelsAgain(t *testing.T) {
	client := &ephemeralWatch{&relaunchWatch{}}
	p, l, s := watchPool(t, client)
	close(s.ready) // the first attach finished; this kernel is the workspace's own

	p.afterReconnect(l, client)
	if got := client.launches(); got != 1 {
		t.Fatalf("the workspace was launched %d time(s) after reconnect, want 1", got)
	}
	if st := p.States()["dev"]; st.Err == "" {
		t.Fatal("a relaunch that failed was not reported on the host")
	}
}

// An SSH kernel outlives the link; the next connect reuses the one still
// running, and launching another would leave two serves for one folder.
func TestAReconnectLeavesAKernelThatOutlivesTheLinkAlone(t *testing.T) {
	client := &relaunchWatch{}
	p, l, s := watchPool(t, client)
	close(s.ready)

	p.afterReconnect(l, client)
	if got := client.launches(); got != 0 {
		t.Fatalf("an ssh kernel was launched %d time(s) after reconnect, want 0", got)
	}
}

// A first attach still bringing the space up owns its launch; a reconnect that
// arrives alongside it must not start a second kernel for the workspace.
func TestAReconnectLeavesAFirstAttachToItsOwnLaunch(t *testing.T) {
	client := &ephemeralWatch{&relaunchWatch{}}
	p, l, _ := watchPool(t, client)

	p.afterReconnect(l, client)
	if got := client.launches(); got != 0 {
		t.Fatalf("a workspace still being attached was launched %d time(s), want 0", got)
	}
}

// kernelFlagsYes is a current kernel's answer to the probe: every flag the
// launch passes, present. Read from the launch's own list rather than copied,
// because a copy is what let a kernel pass the probe and fail to start.
func kernelFlagsYes() string {
	var b strings.Builder
	for _, f := range bootstrap.LaunchFlags(true) {
		b.WriteString("flag " + f + " yes\n")
	}
	return b.String()
}

// A kernel that stops answering is brought back by the request path. The link
// can still be Connected — an SSH connection outlives the serve it reached — so
// the only evidence is the endpoint not answering.
func TestEnsureEndpointRelaunchesAKernelThatStoppedAnswering(t *testing.T) {
	m := fakeMachine(t)
	p := testPool(t, m)
	ws := m.workspace(t, "proj")
	first := mustAttach(t, p, "box", ws)
	defer first.Release()

	m.kill()
	// A liveness answer is trusted for ensureProbeTTL; let the window pass so
	// this request is the one that discovers the kernel is gone.
	time.Sleep(ensureProbeTTL + 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live, ok := p.EnsureEndpoint(ctx, "box", ws)
	if !ok {
		t.Fatal("EnsureEndpoint found nothing after the kernel died")
	}
	if live.Addr == first.Addr {
		t.Fatalf("EnsureEndpoint kept the dead endpoint %s", live.Addr)
	}
	resp, err := http.Get("http://" + live.Addr + "/status")
	if err != nil {
		t.Fatalf("call the replaced kernel: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replaced kernel answered %d", resp.StatusCode)
	}
	if n := m.launches(); n != 2 {
		t.Fatalf("launched %d time(s) after the kernel died, want 2", n)
	}

	// A second attach goes through the same check, finds the repaired kernel,
	// and must not start a third.
	again := mustAttach(t, p, "box", ws)
	defer again.Release()
	if again.Addr != live.Addr || again.Token != live.Token {
		t.Fatalf("second attach = %s, want the repaired endpoint %s", again.Addr, live.Addr)
	}
	if n := m.launches(); n != 2 {
		t.Fatalf("second attach launched again (%d launches)", n)
	}
}

// A pane opened earlier keeps calling the same workspace after the host's idle
// policy is edited. The next attach must replace the kernel rather than hand
// back one launched under the old policy.
func TestSecondAttachReplacesTheKernelWhenTheHostIdlePolicyChanged(t *testing.T) {
	m := fakeMachine(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	writeRemoteHostConfig(t, home, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := NewPool(ctx, Options{Dial: m.dial})
	t.Cleanup(p.Close)

	ws := m.workspace(t, "proj")
	first := mustAttach(t, p, "box", ws)
	defer first.Release()
	if n := m.launches(); n != 1 {
		t.Fatalf("first attach launched %d time(s)", n)
	}

	writeRemoteHostConfig(t, home, -1)
	second := mustAttach(t, p, "box", ws)
	defer second.Release()
	if second.Addr == first.Addr {
		t.Fatalf("the kernel launched with the old idle policy was reused at %s", second.Addr)
	}
	if n := m.launches(); n != 2 {
		t.Fatalf("launched %d time(s) for the changed policy, want 2", n)
	}

	p.mu.Lock()
	client := p.links["box"].client
	p.mu.Unlock()
	st, alive, err := bootstrap.Status(ctx, client, second.Workspace)
	if err != nil {
		t.Fatalf("read the replaced kernel's state: %v", err)
	}
	if !alive || st.IdleTimeoutSeconds != -1 {
		t.Fatalf("state = %+v alive=%v, want the -1 policy the edit asked for", st, alive)
	}
}

// A replacement that fails has not applied the edit. The old kernel is still
// running, and the next attach has to try the edit again instead of reading
// the link's prematurely committed policy as unchanged and silently reuse it.
func TestAFailedPolicyReplacementIsRetriedOnTheNextAttach(t *testing.T) {
	m := fakeMachine(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	writeRemoteHostPolicy(t, home, 10, "auto")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := NewPool(ctx, Options{Dial: m.dial})
	t.Cleanup(p.Close)

	ws := m.workspace(t, "proj")
	first := mustAttach(t, p, "box", ws)
	defer first.Release()

	// The edited policy needs a --idle-timeout flag the remote binary lacks,
	// and install is disabled, so the replacement cannot happen.
	writeRemoteHostPolicy(t, home, -1, "never")
	m.withoutIdleFlag()

	if _, err := p.Attach(context.Background(), "box", ws, Call{}); err == nil {
		t.Fatal("the replacement succeeded with a kernel that cannot take the policy")
	}
	if n := m.launches(); n != 1 {
		t.Fatalf("launched %d time(s) for the failed replacement, want 1", n)
	}

	// The first pane still holds this workspace, so the same space is reused.
	if _, err := p.Attach(context.Background(), "box", ws, Call{}); err == nil {
		t.Fatal("the next attach reused the kernel the failed edit could not replace")
	}
	if n := m.launches(); n != 1 {
		t.Fatalf("the retry launched %d time(s), want 1", n)
	}
}

// A workspace opened for the first time is also an attach: it uses the host's
// policy as it stands now, not the one read when the link was dialed.
func TestANewWorkspaceUsesTheEditedHostPolicy(t *testing.T) {
	m := fakeMachine(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	writeRemoteHostConfig(t, home, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := NewPool(ctx, Options{Dial: m.dial})
	t.Cleanup(p.Close)

	first := mustAttach(t, p, "box", m.workspace(t, "one"))
	defer first.Release()
	writeRemoteHostConfig(t, home, -1)
	second := mustAttach(t, p, "box", m.workspace(t, "two"))
	defer second.Release()

	p.mu.Lock()
	client := p.links["box"].client
	p.mu.Unlock()
	st, alive, err := bootstrap.Status(ctx, client, second.Workspace)
	if err != nil {
		t.Fatalf("read the new workspace's state: %v", err)
	}
	if !alive || st.IdleTimeoutSeconds != -1 {
		t.Fatalf("state = %+v alive=%v, want the edited -1 policy", st, alive)
	}
}

// A kernel relaunched from the request path is another attach opportunity:
// it too has to pick up a config edit made after the link was dialed.
func TestARelaunchedKernelUsesTheEditedHostPolicy(t *testing.T) {
	m := fakeMachine(t)
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	writeRemoteHostConfig(t, home, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := NewPool(ctx, Options{Dial: m.dial})
	t.Cleanup(p.Close)

	ws := m.workspace(t, "proj")
	first := mustAttach(t, p, "box", ws)
	defer first.Release()

	writeRemoteHostConfig(t, home, -1)
	m.kill()
	// A liveness answer is trusted for ensureProbeTTL; let it lapse so this
	// request discovers the kernel is gone.
	time.Sleep(ensureProbeTTL + 100*time.Millisecond)

	live, ok := p.EnsureEndpoint(context.Background(), "box", ws)
	if !ok || live.Addr == "" {
		t.Fatal("EnsureEndpoint did not relaunch the kernel")
	}
	if n := m.launches(); n != 2 {
		t.Fatalf("launched %d time(s), want the relaunched kernel", n)
	}
	p.mu.Lock()
	client := p.links["box"].client
	p.mu.Unlock()
	st, alive, err := bootstrap.Status(ctx, client, live.Workspace)
	if err != nil {
		t.Fatalf("read the relaunched kernel's state: %v", err)
	}
	if !alive || st.IdleTimeoutSeconds != -1 {
		t.Fatalf("state = %+v alive=%v, want the edited -1 policy", st, alive)
	}
}
