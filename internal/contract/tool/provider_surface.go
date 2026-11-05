package tool

import "strings"

// SetProviderVisibleTools restricts the provider-visible schema surface to the
// given names while keeping all registered tools executable via Get. Passing
// nil clears the restriction. Names are normalized with strings.TrimSpace. An
// explicit set is the caller's whole surface for the moment, so it also lifts a
// pin left by SetProviderVisibleToolsExclusive.
func (r *Registry) SetProviderVisibleTools(names []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerExclusive = false
	r.providerSurfaceAuthoritative = false
	r.setProviderVisibleToolsLocked(names)
}

// SetProviderVisibleToolsAuthoritative replaces the surface with one declared
// by a source whose list is the whole answer, such as a recorded composition
// binding, and records that fact for callers that must not treat a default as
// the decision. Like any explicit set it lifts an exclusive pin; nil clears
// both the restriction and the mark.
func (r *Registry) SetProviderVisibleToolsAuthoritative(names []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerExclusive = false
	r.setProviderVisibleToolsLocked(names)
	r.providerSurfaceAuthoritative = names != nil
}

// ProviderSurfaceAuthoritative reports whether the current allowlist came from
// SetProviderVisibleToolsAuthoritative. An unrestricted registry, an advisory
// default and an exclusive pin all answer false.
func (r *Registry) ProviderSurfaceAuthoritative() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerVisible != nil && r.providerSurfaceAuthoritative && !r.providerExclusive
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
