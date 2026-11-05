package tool

import "strings"

// ProviderSurfaceMode describes how a registry's provider-visible restriction
// was established; it decides whether a delegated child inherits it.
type ProviderSurfaceMode uint8

const (
	// SurfaceOpen means no restriction: every registered tool is visible.
	SurfaceOpen ProviderSurfaceMode = iota
	// SurfaceAdvisory is a default later code may widen; children keep their own surface.
	SurfaceAdvisory
	// SurfaceAuthoritative is the host's decision; children intersect their tools with it.
	SurfaceAuthoritative
	// SurfaceExclusive is a closed pin for this provider; children ignore it.
	SurfaceExclusive
)

// ProviderSurfaceMode reports how the current provider-visible restriction was
// set. A registry with no restriction reports SurfaceOpen.
func (r *Registry) ProviderSurfaceMode() ProviderSurfaceMode {
	if r == nil {
		return SurfaceOpen
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.providerVisible == nil {
		return SurfaceOpen
	}
	return r.providerSurface
}

// ProviderSurfaceAuthoritative reports whether the current allowlist is the
// host's declared decision rather than an advisory default or a closed pin. It
// is the boolean projection of ProviderSurfaceMode: an unrestricted registry,
// an advisory default and an exclusive pin all answer false.
func (r *Registry) ProviderSurfaceAuthoritative() bool {
	return r.ProviderSurfaceMode() == SurfaceAuthoritative
}

// SetProviderVisibleTools restricts the provider-visible schema surface to the
// given names while keeping all registered tools executable via Get. Passing
// nil clears the restriction. Names are normalized with strings.TrimSpace. The
// restriction is advisory: later host decisions may widen it, and delegated
// children are not narrowed to it.
func (r *Registry) SetProviderVisibleTools(names []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerSurface = SurfaceAdvisory
	if names == nil {
		r.providerSurface = SurfaceOpen
	}
	r.setProviderVisibleToolsLocked(names)
}

// setProviderVisibleToolsLocked replaces the restriction. Callers hold the
// write lock.
func (r *Registry) setProviderVisibleToolsLocked(names []string) {
	if names == nil {
		if r.providerVisible != nil {
			r.providerVisible = nil
			r.schemaRev.Add(1)
		}
		return
	}
	visible := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			visible[name] = true
		}
	}
	changed := len(visible) != len(r.providerVisible) || r.providerVisible == nil
	if !changed {
		for name := range visible {
			if !r.providerVisible[name] {
				changed = true
				break
			}
		}
	}
	r.providerVisible = visible
	if changed {
		r.schemaRev.Add(1)
	}
}

// SetProviderVisibleToolsAuthoritative restricts the provider-visible surface
// and marks it as the host's decision: delegated children intersect their tools
// with it, and the binding keeps travelling down. It is not a closed pin, so a
// later host decision (PTC re-adding run_code) may still extend it.
func (r *Registry) SetProviderVisibleToolsAuthoritative(names []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerSurface = SurfaceAuthoritative
	if names == nil {
		r.providerSurface = SurfaceOpen
	}
	r.setProviderVisibleToolsLocked(names)
}

// SetProviderVisibleToolsExclusive restricts the provider-visible schema
// surface to names and pins it: until another explicit set replaces it,
// ShowToProvider refuses to add names and AddProviderVisibleTools does nothing.
// An exclusive program surface has exactly one advertised tool, and a switch
// flipped after boot (the peer tools) must not quietly widen it.
func (r *Registry) SetProviderVisibleToolsExclusive(names []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerSurface = SurfaceExclusive
	if names == nil {
		r.providerSurface = SurfaceOpen
	}
	r.setProviderVisibleToolsLocked(names)
}
