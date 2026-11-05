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

// CopyProviderVisibilityFrom mirrors src's provider-visible restriction,
// including the exclusive pin. A sub-agent registry is a new object built from
// the parent's tools; without this copy an exclusive surface would vanish in
// the child, which would then show and call every tool the parent hid.
func (r *Registry) CopyProviderVisibilityFrom(src *Registry) {
	if r == nil || src == nil {
		return
	}
	src.mu.RLock()
	var visible map[string]bool
	if src.providerVisible != nil {
		visible = make(map[string]bool, len(src.providerVisible))
		for name := range src.providerVisible {
			visible[name] = true
		}
	}
	exclusive := src.providerExclusive
	src.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerVisible = visible
	r.providerExclusive = exclusive
	r.schemaRev.Add(1)
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
	r.providerExclusive = names != nil
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
	if r.providerVisible == nil || r.providerExclusive || r.providerVisible[name] {
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
	if r.providerVisible == nil || (shown && r.providerExclusive) {
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
