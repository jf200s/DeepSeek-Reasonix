package serve

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// idleTestClock is a manually advanced clock for the tracker's injected now.
type idleTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newIdleTestClock() *idleTestClock {
	return &idleTestClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *idleTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *idleTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// idleCancelRecorder records the first idle cancel, and ignores repeats.
type idleCancelRecorder struct {
	once sync.Once
	ch   chan struct{}
}

func newIdleCancelRecorder() *idleCancelRecorder {
	return &idleCancelRecorder{ch: make(chan struct{})}
}

func (r *idleCancelRecorder) cancel() {
	r.once.Do(func() { close(r.ch) })
}

func (r *idleCancelRecorder) wait(t *testing.T) bool {
	t.Helper()
	select {
	case <-r.ch:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// No client at all is already an idle server: after the timeout it must cancel.
func TestIdleTrackerFiresWithNoClients(t *testing.T) {
	clock := newIdleTestClock()
	cancel := newIdleCancelRecorder()
	tracker := newIdleTracker(30*time.Second, cancel.cancel, clock.Now)
	defer tracker.Stop()

	clock.Advance(29 * time.Second)
	if tracker.checkIdle() {
		t.Fatal("fired before the timeout elapsed")
	}
	select {
	case <-cancel.ch:
		t.Fatal("cancelled before the timeout elapsed")
	default:
	}

	clock.Advance(time.Second)
	if !tracker.checkIdle() {
		t.Fatal("did not fire after the timeout elapsed")
	}
	if !cancel.wait(t) {
		t.Fatal("cancel was never called")
	}
}

// A handler that stays in flight is an active client: the tracker must not
// fire while it runs, even if the wall clock moves far past the timeout.
func TestIdleTrackerKeepsALongRequestAlive(t *testing.T) {
	clock := newIdleTestClock()
	cancel := newIdleCancelRecorder()
	tracker := newIdleTracker(5*time.Second, cancel.cancel, clock.Now)
	defer tracker.Stop()

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	handler := tracker.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	}))
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		close(finished)
	}()

	<-started
	clock.Advance(time.Hour)
	if tracker.checkIdle() {
		t.Fatal("fired while a request was still in flight")
	}
	select {
	case <-cancel.ch:
		t.Fatal("cancelled while a request was still in flight")
	default:
	}

	close(release)
	<-finished
	if tracker.checkIdle() {
		t.Fatal("fired immediately after the request ended, before its timeout")
	}
	clock.Advance(5 * time.Second)
	if !tracker.checkIdle() {
		t.Fatal("did not fire once the request was over and the timeout elapsed")
	}
	if !cancel.wait(t) {
		t.Fatal("cancel was never called")
	}
}

// A zero timeout means "exit as soon as there are zero clients", which an
// untouched tracker already is.
func TestIdleTrackerZeroTimeoutFiresAtZeroClients(t *testing.T) {
	clock := newIdleTestClock()
	cancel := newIdleCancelRecorder()
	tracker := newIdleTracker(0, cancel.cancel, clock.Now)
	defer tracker.Stop()

	if !tracker.checkIdle() {
		t.Fatal("zero timeout did not fire with no clients")
	}
	if !cancel.wait(t) {
		t.Fatal("cancel was never called")
	}
}

// End to end through the real listener: one request, then the 150ms idle
// timeout drains the server and RunGracefulListener returns normally.
func TestHubIdleTimeoutShutsDownAfterRequest(t *testing.T) {
	hub := NewHub(HubOptions{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hub.SetIdleTimeout(150*time.Millisecond, nil)

	done := make(chan error, 1)
	go func() { done <- hub.RunGracefulListener(context.Background(), ln) }()

	addr := ln.Addr().String()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	answered := false
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/")
		if err == nil {
			_ = resp.Body.Close()
			answered = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !answered {
		t.Fatalf("server on %s never answered", addr)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunGracefulListener returned %v, want nil after idle drain", err)
		}
	case <-time.After(5 * time.Second):
		hub.SetIdleTimeout(-1, nil)
		t.Fatal("server did not shut down after the idle timeout")
	}
}
