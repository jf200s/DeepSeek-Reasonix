package tool

import "testing"

type reachingTool struct {
	stubTool
	reach Reach
}

func (r reachingTool) Reach() Reach { return r.reach }

func admitReads(t Tool) bool { return ReachOf(t) == ReachLocalRead }

func TestRestrictDropsWhatIsHeldAndRefusesWhatComesLater(t *testing.T) {
	r := NewRegistry()
	r.Add(reachingTool{stubTool{name: "read"}, ReachLocalRead})
	r.Add(stubTool{name: "unclassified"})
	r.Add(reachingTool{stubTool{name: "control"}, ReachHostControl})
	before := r.SchemaRevision()

	r.Restrict(admitReads)
	if got := r.AllNames(); len(got) != 1 || got[0] != "read" {
		t.Fatalf("after Restrict the registry holds %v, want only read", got)
	}
	if r.SchemaRevision() == before {
		t.Fatal("dropping tools left the schema revision unchanged")
	}

	r.Add(stubTool{name: "late"})
	r.Add(reachingTool{stubTool{name: "read2"}, ReachLocalRead})
	r.Add(reachingTool{stubTool{name: "read"}, ReachHostControl})
	if got := r.AllNames(); len(got) != 2 || got[0] != "read" || got[1] != "read2" {
		t.Fatalf("registry holds %v, want read and read2 in order", got)
	}
	if tl, ok := r.Get("read"); !ok || ReachOf(tl) != ReachLocalRead {
		t.Fatal("a rejected replacement displaced an admitted tool")
	}
	if _, ok := r.Get("unclassified"); ok {
		t.Fatal("a tool the ceiling refuses is still resolvable")
	}
}

func TestReachOfIsUnstatedUntilDeclared(t *testing.T) {
	if got := ReachOf(stubTool{name: "x"}); got != ReachUnstated {
		t.Fatalf("ReachOf = %v, want the zero value", got)
	}
}

func surfaceNames(r *Registry) []string {
	out := make([]string, 0)
	for _, s := range r.Schemas() {
		out = append(out, s.Name)
	}
	return out
}

func sameNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The setter decides the surface's standing, not only its names: advisory is a
// default later code may widen, authoritative is what delegated children
// inherit by intersection, and exclusive is the closed pin PTC relies on.
func TestProviderSurfaceModesFollowTheirSetter(t *testing.T) {
	r := NewRegistry()
	r.Add(stubTool{name: "a"})
	r.Add(stubTool{name: "b"})

	if got := r.ProviderSurfaceMode(); got != SurfaceOpen {
		t.Fatalf("empty registry mode = %v, want open", got)
	}

	r.SetProviderVisibleTools([]string{"a"})
	if got := r.ProviderSurfaceMode(); got != SurfaceAdvisory {
		t.Fatalf("SetProviderVisibleTools mode = %v, want advisory", got)
	}
	if got := surfaceNames(r); !sameNames(got, "a") {
		t.Fatalf("advisory surface = %v, want [a]", got)
	}

	r.SetProviderVisibleToolsAuthoritative([]string{"b"})
	if got := r.ProviderSurfaceMode(); got != SurfaceAuthoritative {
		t.Fatalf("SetProviderVisibleToolsAuthoritative mode = %v, want authoritative", got)
	}
	if got := surfaceNames(r); !sameNames(got, "b") {
		t.Fatalf("authoritative surface = %v, want [b]", got)
	}
	// Not a pin: a later host decision (PTC's run_code) may extend it.
	r.AddProviderVisibleTools("a")
	if got := surfaceNames(r); !sameNames(got, "a", "b") {
		t.Fatalf("widened authoritative surface = %v, want [a b]", got)
	}

	r.SetProviderVisibleToolsExclusive([]string{"a"})
	if got := r.ProviderSurfaceMode(); got != SurfaceExclusive {
		t.Fatalf("SetProviderVisibleToolsExclusive mode = %v, want exclusive", got)
	}
	r.AddProviderVisibleTools("b")
	r.ShowToProvider(true, "b")
	if got := surfaceNames(r); !sameNames(got, "a") {
		t.Fatalf("exclusive surface was widened to %v", got)
	}

	r.SetProviderVisibleTools(nil)
	if got := r.ProviderSurfaceMode(); got != SurfaceOpen {
		t.Fatalf("cleared surface mode = %v, want open", got)
	}
	if got := surfaceNames(r); !sameNames(got, "a", "b") {
		t.Fatalf("cleared surface = %v, want both tools", got)
	}
}
