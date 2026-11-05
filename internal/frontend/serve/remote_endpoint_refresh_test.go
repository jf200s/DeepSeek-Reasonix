package serve

import (
	"context"
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

// ensuringAttacher repairs its kernel on demand and counts how often the proxy
// asked. It is the link layer that can bring a dead kernel back, unlike the
// resolver-only attachers the other tests use.
type ensuringAttacher struct {
	stubAttacher

	mu      sync.Mutex
	addrs   []string
	ensures atomic.Int32
}

func (a *ensuringAttacher) EnsureEndpoint(_ context.Context, host, workspace string) (RemoteEndpoint, bool) {
	n := a.ensures.Add(1)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.addrs) == 0 {
		return RemoteEndpoint{}, false
	}
	addr := a.addrs[0]
	if int(n) < len(a.addrs) {
		addr = a.addrs[n-1]
	}
	return RemoteEndpoint{Host: host, Workspace: workspace, Addr: addr, Token: "remote-secret"}, true
}

// The request path asks the link layer before proxying, and one request uses
// exactly one answer: a kernel relaunched between the probe and the send was
// not consulted twice, and the address the proxy used is the address the error
// handler names.
func TestARemoteRequestProxiesTheEndpointEnsureJustReturned(t *testing.T) {
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "the kernel ensure returned: "+r.URL.Path)
	}))
	defer live.Close()

	att := &ensuringAttacher{}
	att.attach = func(host, workspace string) (RemoteEndpoint, func(), error) {
		return RemoteEndpoint{Host: host, Workspace: workspace, Addr: "127.0.0.1:1", Token: "remote-secret"}, func() {}, nil
	}
	att.addrs = []string{live.Listener.Addr().String()}

	h := NewHub(HubOptions{Remote: att})
	rt := remotePane(t, h, "127.0.0.1:1")
	front := httptest.NewServer(operatorHandler(h))
	defer front.Close()

	resp, err := http.Get(front.URL + rt.view().Base + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "the kernel ensure returned: /status" {
		t.Fatalf("pane answered %q, want the endpoint EnsureEndpoint returned", got)
	}
	if got := att.ensures.Load(); got != 1 {
		t.Fatalf("EnsureEndpoint ran %d time(s) for one request, want 1", got)
	}
}

// A request that fails after it was sent is reported once and never replayed:
// a POST that reached the kernel must not be sent again by the proxy. The next
// request is free to ask for a fresh endpoint and recover.
func TestARemotePostThatFailsAfterSendingIsNotReplayed(t *testing.T) {
	t.Setenv("REASONIX_HOME", testenv.TempDir(t))
	var hits atomic.Int32
	kernel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			// Take the request, then drop the connection without an answer, the
			// shape of a kernel that dies after accepting a POST.
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test kernel cannot hijack its connection")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = io.WriteString(w, "recovered: "+r.URL.Path)
	}))
	defer kernel.Close()

	att := &ensuringAttacher{}
	att.attach = func(host, workspace string) (RemoteEndpoint, func(), error) {
		return RemoteEndpoint{Host: host, Workspace: workspace, Addr: kernel.Listener.Addr().String(), Token: "remote-secret"}, func() {}, nil
	}
	att.addrs = []string{kernel.Listener.Addr().String()}

	h := NewHub(HubOptions{Remote: att})
	rt := remotePane(t, h, kernel.Listener.Addr().String())
	front := httptest.NewServer(operatorHandler(h))
	defer front.Close()

	resp, err := http.Post(front.URL+rt.view().Base+"/turns", "application/json", strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed POST = %d, want 502", resp.StatusCode)
	}
	var reason Reason
	if err := json.NewDecoder(resp.Body).Decode(&reason); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if reason.Code != "remote.unreachable" || reason.Params["host"] != "gpu-box" {
		t.Fatalf("refusal = %+v, want remote.unreachable naming the host", reason)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("the kernel received the POST %d time(s), want 1", got)
	}

	// The next request resolves the endpoint again and reaches the kernel.
	answer, err := http.Get(front.URL + rt.view().Base + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer answer.Body.Close()
	body, _ := io.ReadAll(answer.Body)
	if got := string(body); got != "recovered: /status" {
		t.Fatalf("next request answered %q, want the kernel again", got)
	}
}
