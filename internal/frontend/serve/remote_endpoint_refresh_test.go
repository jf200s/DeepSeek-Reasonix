package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"reasonix/internal/base/testenv"
)

// movingAttacher is a link layer whose kernel moved. The pane keeps the
// endpoint it was opened on; Resolve is the one place that says where that
// workspace answers now.
type movingAttacher struct {
	stubAttacher

	mu   sync.Mutex
	addr string
	hits atomic.Int32
}

func (m *movingAttacher) move(addr string) {
	m.mu.Lock()
	m.addr = addr
	m.mu.Unlock()
}

func (m *movingAttacher) Resolve(host, workspace string) (RemoteEndpoint, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addr == "" {
		return RemoteEndpoint{}, false
	}
	return RemoteEndpoint{Host: host, Workspace: workspace, Addr: m.addr, Token: "remote-secret"}, true
}

// A WSL kernel is launched again on a new port when its link comes back, and a
// pane opened before that still holds the old address. The proxy has to ask
// where the workspace answers now rather than keep calling the one that died.
func TestARelaunchedKernelIsReachedThroughThePanesOldProxy(t *testing.T) {
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	saveTestRemoteHosts(t, "gpu-box")

	old := fakeRemoteKernel(t)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "the kernel that answered after the reconnect: "+r.URL.Path)
	}))
	defer live.Close()

	att := &movingAttacher{}
	att.attach = func(host, workspace string) (RemoteEndpoint, func(), error) {
		return RemoteEndpoint{
			Host:      host,
			Workspace: workspace,
			Addr:      old.Listener.Addr().String(),
			Token:     "remote-secret",
		}, func() {}, nil
	}
	h := NewHub(HubOptions{Remote: att})
	front := httptest.NewServer(operatorHandler(h))
	defer front.Close()

	resp, err := http.Post(front.URL+"/remotes/open", "application/json",
		strings.NewReader(`{"host":"gpu-box","workspace":"/srv/data/training"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open = %d, want 200", resp.StatusCode)
	}
	var view RuntimeView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}

	att.move(live.Listener.Addr().String())
	answer, err := http.Get(front.URL + view.Base + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer answer.Body.Close()
	body, _ := io.ReadAll(answer.Body)
	if got := string(body); got != "the kernel that answered after the reconnect: /status" {
		t.Fatalf("the pane answered %q, want the relaunched kernel", got)
	}
}

// A tree read on a Windows-spelled workspace reuses a pane the picker opened
// on its /mnt form. Comparing the two spellings directly never matched, so
// every read dialled again.
func TestAWindowsWorkspaceReadUsesAPaneOpenedOnItsMntForm(t *testing.T) {
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	var attaches atomic.Int32
	att := &stubAttacher{attach: func(host, workspace string) (RemoteEndpoint, func(), error) {
		attaches.Add(1)
		return RemoteEndpoint{Host: host, Workspace: workspace, Addr: "127.0.0.1:1", Token: "remote-secret"}, func() {}, nil
	}}
	h := NewHub(HubOptions{Remote: att})
	front := httptest.NewServer(operatorHandler(h))
	defer front.Close()
	bookPost(t, front, "/remotes", `{"name":"gpu-box","host":"example.invalid","workspace":"C:\\proj"}`)

	if _, err := h.OpenRemote(RemoteEndpoint{
		Host:      "gpu-box",
		Workspace: "/mnt/c/proj",
		Addr:      "127.0.0.1:9",
		Token:     "remote-secret",
	}, func() {}); err != nil {
		t.Fatal(err)
	}

	ep, release, err := h.remoteLink(httptest.NewRequest(http.MethodGet, "/", nil), "gpu-box")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if ep.Workspace != "/mnt/c/proj" {
		t.Fatalf("workspace = %q, want the pane's /mnt spelling", ep.Workspace)
	}
	if got := attaches.Load(); got != 0 {
		t.Fatalf("the read dialled %d time(s), want the open pane's link", got)
	}
}
