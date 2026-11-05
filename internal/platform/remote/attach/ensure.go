// ensure.go — keeping a held workspace's kernel alive. Resolve answers from the
// snapshot a pane holds; this file is what makes that snapshot true again.
package attach

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"reasonix/internal/contract/config"
	"reasonix/internal/platform/remote/bootstrap"
)

// ensureProbeTTL is how long one liveness answer is trusted, so a pane making
// several calls at once does not probe the kernel for each of them.
const ensureProbeTTL = time.Second

// ensureProbeTimeout bounds the HTTP probe. A kernel on the far side of a
// forward answers quickly or not at all; waiting longer only delays the
// relaunch a dead kernel needs.
const ensureProbeTimeout = 750 * time.Millisecond

// remoteTokenCookie mirrors serve.TokenCookie. The platform layer cannot import
// the frontend that owns the name, and the probe must present the same cookie
// the proxy does.
const remoteTokenCookie = "reasonix_token"

// errKernelUnavailable says a cached probe found nothing to talk to. It is
// deliberately not the launch error: the caller only needs to know there is no
// endpoint to use.
var errKernelUnavailable = errors.New("attach: the remote kernel is not answering")

// probeEndpoint asks a forwarded kernel whether it is still there. Any HTTP
// answer below 500 proves the listener and its forward, and the token cookie
// keeps an unrelated process on a recycled port from passing as the kernel.
func probeEndpoint(ctx context.Context, addr, token string) bool {
	if strings.TrimSpace(addr) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, ensureProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/status", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Cookie", remoteTokenCookie+"="+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode < http.StatusInternalServerError
}

// EnsureEndpoint reports where a workspace answers now, bringing its kernel
// back first when the probe finds it gone. Resolve stays the read-only
// snapshot; this is the request path's entry point, and it does its remote work
// outside the pool's lock so one slow machine cannot stall the others.
func (p *Pool) EnsureEndpoint(ctx context.Context, host, workspace string) (Endpoint, bool) {
	p.mu.Lock()
	l := p.links[host]
	var s *space
	if l != nil {
		for _, cand := range l.spaces {
			if cand.key == workspace || cand.workspace == workspace {
				s = cand
				break
			}
		}
	}
	p.mu.Unlock()
	if l == nil || s == nil {
		return Endpoint{}, false
	}
	// A first attach still producing this space owns its launch.
	select {
	case <-s.ready:
	default:
		return Endpoint{}, false
	}
	if err := p.ensureSpace(ctx, l, s); err != nil {
		return Endpoint{}, false
	}
	p.mu.Lock()
	addr, token, workspace := s.addr, s.token, s.workspace
	p.mu.Unlock()
	if strings.TrimSpace(addr) == "" {
		return Endpoint{}, false
	}
	return Endpoint{Host: host, Workspace: workspace, Addr: addr, Token: token}, true
}

// ensureAttach re-reads the host's policy for a workspace whose kernel is
// already recorded. A changed idle timeout or install strategy asks for a
// launch — the bootstrap decides whether the running kernel can stay — while
// an unchanged policy only relaunches a kernel that stopped answering.
func (p *Pool) ensureAttach(ctx context.Context, l *link, s *space, call Call) error {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.err != nil {
		return s.err
	}
	if p.refreshHostPolicy(l) {
		s.ensure.Lock()
		defer s.ensure.Unlock()
		return p.launch(ctx, l, s, s.key, call)
	}
	return p.ensureSpace(ctx, l, s)
}

// ensureSpace makes sure s still has a live kernel, probing at most once per
// ensureProbeTTL and relaunching under the space's own lock when it does not.
func (p *Pool) ensureSpace(ctx context.Context, l *link, s *space) error {
	s.ensure.Lock()
	defer s.ensure.Unlock()
	if s.err != nil {
		return s.err
	}
	if time.Since(s.probedAt) < ensureProbeTTL {
		if s.probedOK {
			return nil
		}
		return errKernelUnavailable
	}
	p.mu.Lock()
	addr, token, workspace := s.addr, s.token, s.workspace
	p.mu.Unlock()
	alive := probeEndpoint(ctx, addr, token)
	if !alive {
		// The HTTP probe is the fast path; a kernel whose status route answers
		// differently still passes the process probe the bootstrap owns.
		if _, ok, err := bootstrap.Status(ctx, l.client, workspace); err == nil && ok {
			alive = true
		}
	}
	s.probedAt, s.probedOK = time.Now(), alive
	if alive {
		return nil
	}
	if err := p.launch(ctx, l, s, s.key, Call{}); err != nil {
		p.record(l, func(st *HostState) { st.Err = err.Error() })
		return err
	}
	s.probedAt, s.probedOK = time.Now(), true
	return nil
}

// refreshHostPolicy re-reads the host entry and reports whether its install
// strategy or idle timeout moved since the link was dialed, so a config edit is
// visible on the next attach without restarting the app.
func (p *Pool) refreshHostPolicy(l *link) bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	entry, ok := cfg.RemoteHost(l.host)
	if !ok {
		return false
	}
	install := entry.ServeInstallMode()
	idle := entry.IdleTimeout(cfg.Remote.IdleTimeoutMinutes)
	p.mu.Lock()
	defer p.mu.Unlock()
	moved := install != l.install || l.idleTimeout == nil || *l.idleTimeout != idle
	l.install, l.idleTimeout = install, &idle
	return moved
}

// hostPolicy reads what a launch should use. The policy moves when a later
// attach re-reads the host entry, so it is read under the pool's lock rather
// than straight off the link.
func (p *Pool) hostPolicy(l *link) (install string, idle *time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return l.install, l.idleTimeout
}
