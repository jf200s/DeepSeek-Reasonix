package tool

import (
	"maps"
	"strings"
)

// BindMCPRuntime applies execution authority to existing and future adapters.
func (r *Registry) BindMCPRuntime(bind func(Tool) Tool) {
	r.mu.Lock()
	r.bindMCP = bind
	held := maps.Clone(r.tools)
	r.mu.Unlock()
	for _, t := range held {
		if _, ok := t.(MCPMetadata); ok {
			r.Add(t)
		}
	}
}

// Restrict fixes the ceiling on what the registry may hold. Tools already
// registered that admit rejects are removed, and every later Add is checked
// against it. admit runs outside the registry lock: a lazy tool's own methods
// may take a lock that a registry swap holds while it waits for this one.
func (r *Registry) Restrict(admit func(Tool) bool) {
	if r == nil || admit == nil {
		return
	}
	r.mu.Lock()
	r.admit = admit
	held := maps.Clone(r.tools)
	r.mu.Unlock()
	rejected := map[string]bool{}
	for name, t := range held {
		if t != nil && !admit(t) {
			rejected[name] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.order[:0]
	for _, name := range r.order {
		if rejected[name] {
			delete(r.tools, name)
			delete(r.canon, name)
			continue
		}
		kept = append(kept, name)
	}
	r.order = kept
	r.schemaRev.Add(1)
}

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

// AddProviderVisibleTools adds name to an existing provider-visible
// restriction. With no restriction every registered tool is already shown, so
// there is nothing to add; nil is a no-op.
func (r *Registry) AddProviderVisibleTools(name string) {
	if r == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providerVisible == nil || r.providerSurface == SurfaceExclusive || r.providerVisible[name] {
		return
	}
	r.providerVisible[name] = true
	r.schemaRev.Add(1)
}

// ShowToProvider adds or removes names from an existing provider-visible
// restriction; with no restriction every registered tool is already shown.
// Adding is refused while the surface is exclusive: shrinking it is always
// allowed, widening it is what the exclusive surface exists to prevent.
func (r *Registry) ShowToProvider(shown bool, names ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providerVisible == nil || (shown && r.providerSurface == SurfaceExclusive) {
		return
	}
	for _, name := range names {
		if r.providerVisible[name] == shown {
			continue
		}
		if shown {
			r.providerVisible[name] = true
		} else {
			delete(r.providerVisible, name)
		}
		r.schemaRev.Add(1)
	}
}

// ProviderVisible reports whether name is currently provider-visible.
func (r *Registry) ProviderVisible(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.providerVisible == nil {
		return true
	}
	return r.providerVisible[strings.TrimSpace(name)]
}

// ProviderSurfacePinned reports whether name belongs to an explicit
// provider-visible allowlist. Unlike ProviderVisible it is false when no
// allowlist is set: a registry that shows whatever it holds promises nothing
// about which names a provider sees, so nothing has to be held back to keep
// that promise.
func (r *Registry) ProviderSurfacePinned(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerVisible != nil && r.providerVisible[strings.TrimSpace(name)]
}

func (r *Registry) isProviderVisibleLocked(name string) bool {
	if r.providerVisible == nil {
		return true
	}
	return r.providerVisible[name]
}

// InheritProviderVisibility applies src's surface to a child registry. An
// authoritative surface narrows the child to the same names plus always: the
// closing protocol, a review verdict and the image reader its own model brings
// are attached after construction and must stay visible even though the binding
// never named them. Other modes leave the child unrestricted.
func (r *Registry) InheritProviderVisibility(src *Registry, always ...string) {
	if r == nil || src == nil {
		return
	}
	src.mu.RLock()
	mode := src.providerSurface
	var visible map[string]bool
	if mode == SurfaceAuthoritative && src.providerVisible != nil {
		visible = maps.Clone(src.providerVisible)
	}
	src.mu.RUnlock()
	if visible == nil {
		return
	}
	for _, name := range always {
		if name = strings.TrimSpace(name); name != "" {
			visible[name] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerVisible = visible
	r.providerSurface = SurfaceAuthoritative
	r.schemaRev.Add(1)
}
