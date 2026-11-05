package serve

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// idleSettings is the per-hub idle configuration. It lives beside the Hub
// rather than in it so a frontend that never asks for idle exit pays nothing
// for the feature. The map is keyed by the hub pointer and lives as long as
// the hub does, which in practice is the process.
var idleSettings sync.Map // map[*Hub]idleSetting

type idleSetting struct {
	timeout time.Duration
	cancel  context.CancelFunc
}

// SetIdleTimeout arms idle self-exit for the hub: when no request has been in
// flight for timeout, cancel is called so RunGracefulListener drains and the
// process returns normally. A negative timeout disarms it. cancel may be nil,
// in which case RunGracefulListener cancels its own serving context.
func (h *Hub) SetIdleTimeout(timeout time.Duration, cancel context.CancelFunc) {
	if h == nil {
		return
	}
	if timeout < 0 {
		idleSettings.Delete(h)
		return
	}
	idleSettings.Store(h, idleSetting{timeout: timeout, cancel: cancel})
}

func (h *Hub) idleSetting() (idleSetting, bool) {
	v, ok := idleSettings.Load(h)
	if !ok {
		return idleSetting{}, false
	}
	return v.(idleSetting), true
}

// idleTracker counts in-flight requests and cancels its context once the
// count has stayed at zero for the configured timeout. A long-lived handler
// (SSE, an upload) keeps the kernel alive for its whole duration. now is
// injectable so tests can advance time without sleeping; production passes
// time.Now, whose monotonic reading Sub measures correctly.
type idleTracker struct {
	timeout time.Duration
	cancel  context.CancelFunc
	now     func() time.Time

	mu        sync.Mutex
	active    int
	idleSince time.Time
	fired     bool
	stopped   bool
	timer     *time.Timer
}

func newIdleTracker(timeout time.Duration, cancel context.CancelFunc, now func() time.Time) *idleTracker {
	if now == nil {
		now = time.Now
	}
	t := &idleTracker{
		timeout:   timeout,
		cancel:    cancel,
		now:       now,
		idleSince: now(),
	}
	t.mu.Lock()
	t.armLocked()
	t.mu.Unlock()
	return t
}

// Wrap reports every request to the tracker for the handler's whole duration.
func (t *idleTracker) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.begin()
		defer t.end()
		next.ServeHTTP(w, r)
	})
}

func (t *idleTracker) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.active++
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

func (t *idleTracker) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	if t.active > 0 {
		t.active--
	}
	if t.active != 0 {
		return
	}
	t.idleSince = t.now()
	t.armLocked()
}

// Check reports whether the server is idle and fires the timeout if so. It is
// the test seam for the injected clock; the timer callback uses the same
// decision below.
func (t *idleTracker) checkIdle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.checkLocked()
}

func (t *idleTracker) checkLocked() bool {
	if t.fired {
		return true
	}
	if t.stopped || t.active > 0 {
		return false
	}
	if t.timeout > 0 && t.now().Sub(t.idleSince) < t.timeout {
		return false
	}
	t.fireLocked()
	return true
}

func (t *idleTracker) armLocked() {
	if t.fired || t.stopped || t.active > 0 {
		return
	}
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	remaining := t.timeout - t.now().Sub(t.idleSince)
	if remaining <= 0 {
		t.fireLocked()
		return
	}
	t.timer = time.AfterFunc(remaining, t.onTimer)
}

func (t *idleTracker) onTimer() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer = nil
	if t.fired || t.stopped || t.active > 0 {
		return
	}
	if remaining := t.timeout - t.now().Sub(t.idleSince); remaining > 0 {
		// The injected clock may say less time has passed than the real timer
		// waited; honour the clock and check again.
		t.timer = time.AfterFunc(remaining, t.onTimer)
		return
	}
	t.fireLocked()
}

// fireLocked logs the exit once and cancels asynchronously: cancel may lead
// back into code that takes the tracker's lock, and nothing here needs to
// wait for the shutdown to begin.
func (t *idleTracker) fireLocked() {
	if t.fired {
		return
	}
	t.fired = true
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	seconds := int64(t.timeout / time.Second)
	slog.Info(fmt.Sprintf("serve: idle timeout reached (%ds without clients); exiting", seconds))
	if t.cancel != nil {
		go t.cancel()
	}
}

// Stop releases the timer. It does not fire after this returns.
func (t *idleTracker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}
