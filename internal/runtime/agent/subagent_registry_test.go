package agent

import (
	"context"
	"encoding/json"
	"reasonix/internal/runtime/usecap"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/contract/tool"
	"reasonix/internal/ext/plugin"
)

func TestFilterCapabilityListResultFailClosed(t *testing.T) {
	// Empty server set must not return the raw full inventory.
	full := `{"servers":[{"name":"secret-db","capability_id":"mcp-server:secret-db","status":"configured","authorized":true,"connected":false}],"note":"all"}`
	out := usecap.FilterCapabilityListResult(full, nil)
	if strings.Contains(out, "secret-db") {
		t.Fatalf("empty servers must fail closed:\n%s", out)
	}
	if !strings.Contains(out, `"servers": []`) && !strings.Contains(out, `"servers":[]`) {
		t.Fatalf("expected empty servers array:\n%s", out)
	}

	// Malformed JSON must not pass through raw text that might contain names.
	leaky := `not-json but mentions secret-db and production`
	out = usecap.FilterCapabilityListResult(leaky, map[string]bool{"alpha": true})
	if strings.Contains(out, "secret-db") || strings.Contains(out, "not-json") {
		t.Fatalf("malformed payload must fail closed:\n%s", out)
	}
	if !strings.Contains(out, `"servers"`) {
		t.Fatalf("fail-closed payload should still be JSON list shape:\n%s", out)
	}
}

func TestRestrictedListWithEmptyServersMapFailClosed(t *testing.T) {
	// Direct unit path: restricted proxy with empty servers still filters list.
	inner := usecap.NewUseCapabilityTool(context.Background(), nil, []plugin.Spec{
		{Name: "secret-db", Authorized: true},
	}, tool.NewRegistry(), nil, nil, nil)
	proxy := &restrictedCapabilityProxy{
		Tool:     inner,
		resolver: inner,
		allowed:  map[string]bool{"mcp-tool:incomplete": true}, // invalid shape should never happen after validation
		servers:  map[string]bool{},
	}
	rc, err := proxy.ResolveCall(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rc.Result, "secret-db") {
		t.Fatalf("empty servers map must not leak inventory:\n%s", rc.Result)
	}
}

func TestSubagentRegistryCarriesDisabledMCPPolicy(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(subagentRegistryTool{name: "read_file", readOnly: true})
	binding := tool.MCPBinding{
		Server:       "mock",
		RawName:      "write",
		VisibleName:  "write",
		CallableName: "mcp__mock__write",
		CapabilityID: "mcp-tool:mock/write",
	}
	parent.ReplaceDisabledMCP("mock", []tool.MCPBinding{binding})

	sub := SubagentToolRegistry(parent, nil)
	if !sub.DisabledMCP(binding.CallableName) || !sub.DisabledMCP(binding.CapabilityID) {
		t.Fatal("subagent registry lost the parent's disabled-tool attribution")
	}
}

func TestRestrictedProxyRefusesInspectOfUnauthorizedID(t *testing.T) {
	inner := usecap.NewUseCapabilityTool(context.Background(), nil, []plugin.Spec{
		{Name: "secret-db", Authorized: true},
	}, tool.NewRegistry(), nil, nil, nil)
	proxy := &restrictedCapabilityProxy{
		Tool:     inner,
		resolver: inner,
		allowed:  map[string]bool{"mcp-tool:mock/read": true},
		servers:  map[string]bool{"mock": true},
	}
	for _, id := range []string{"mcp-server:secret-db", "mcp-tool:secret-db/query"} {
		args := json.RawMessage(`{"action":"inspect","capability_id":"` + id + `"}`)
		if _, err := proxy.ResolveCall(context.Background(), args); err == nil || !strings.Contains(err.Error(), "outside this subagent's allowed-tools") {
			t.Errorf("inspect %s err = %v, want allowed-tools refusal", id, err)
		}
	}
}

// visTool is the smallest Tool a visibility test needs.
type visTool struct{ name string }

func (v visTool) Name() string                                             { return v.name }
func (v visTool) Description() string                                      { return v.name }
func (v visTool) Schema() json.RawMessage                                  { return json.RawMessage(`{"type":"object"}`) }
func (v visTool) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }
func (v visTool) ReadOnly() bool                                           { return true }

// childSchemaNames is the child's provider-visible surface, sorted.
func childSchemaNames(reg *tool.Registry) []string {
	out := make([]string, 0)
	for _, schema := range reg.Schemas() {
		out = append(out, schema.Name)
	}
	slices.Sort(out)
	return out
}

// The boot-time unified surface is advisory: it shapes what the main model is
// offered and says nothing about what a delegated child inherits. A child runs
// its own loop with its own allowlist, so a profile tool the parent's default
// surface hides — and the protocol tools attached after construction — reach
// the child's provider.
func TestFilterRegistryLeavesAnAdvisorySurfaceToTheChild(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(visTool{"read_file"})
	parent.Add(visTool{"specialist_tool"})
	parent.SetProviderVisibleTools([]string{"read_file"})

	child := SubagentToolRegistry(parent, nil)
	child.Add(visTool{"view_image"}) // what childTools contributes for an image model
	AttachReviewReportTool(child, ReviewReportGrant{})

	want := []string{"complete_subtask", "read_file", "review_report", "specialist_tool", "view_image"}
	if got := childSchemaNames(child); !slices.Equal(got, want) {
		t.Fatalf("advisory child schema = %v, want %v", got, want)
	}
}

// A composition binding is authoritative: a tool it hides stays hidden from
// delegated children, while the protocol and model-capability tools a child
// owes are always added back even though the binding never named them.
func TestFilterRegistryIntersectsAnAuthoritativeSurface(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(visTool{"read_file"})
	parent.Add(visTool{"hidden_writer"})
	parent.SetProviderVisibleToolsAuthoritative([]string{"read_file"})

	child := SubagentToolRegistry(parent, nil)
	child.Add(visTool{"view_image"})
	AttachReviewReportTool(child, ReviewReportGrant{})

	want := []string{"complete_subtask", "read_file", "review_report", "view_image"}
	if got := childSchemaNames(child); !slices.Equal(got, want) {
		t.Fatalf("authoritative child schema = %v, want %v", got, want)
	}
	if child.ProviderVisible("hidden_writer") {
		t.Fatal("a binding-hidden tool was offered to the child")
	}
	// A profile allowlist cannot expand the binding either.
	narrowed := SubagentToolRegistry(parent, []string{"hidden_writer"})
	if narrowed.ProviderVisible("hidden_writer") {
		t.Fatal("a child allowlist widened an authoritative binding")
	}

	// The binding keeps travelling down: a grandchild intersects with the
	// child's surface, so the hidden tool never reappears.
	grandchild := SubagentToolRegistry(child, nil)
	if grandchild.ProviderVisible("hidden_writer") {
		t.Fatal("an authoritative binding stopped at the first delegation layer")
	}
}

// An exclusive program surface is a decision about the main model only. A
// child is not in PTC mode, so the pin is not mirrored onto it; the child's own
// toolset decides, and the schema is never left empty.
func TestFilterRegistryDoesNotInheritAnExclusiveSurface(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(visTool{"run_code"})
	parent.Add(visTool{"read_file"})
	parent.Add(visTool{"grep"})
	parent.SetProviderVisibleToolsExclusive([]string{"run_code"})

	child := SubagentToolRegistry(parent, nil)
	got := childSchemaNames(child)
	if len(got) == 0 {
		t.Fatal("the child inherited an exclusive surface and was left with an empty schema")
	}
	if slices.Contains(got, "run_code") {
		t.Fatalf("run_code reached a subagent schema: %v", got)
	}
	for _, want := range []string{"complete_subtask", "grep", "read_file"} {
		if !slices.Contains(got, want) {
			t.Fatalf("exclusive-parent child schema %v lost %q", got, want)
		}
	}
	// The pin still holds where it was declared: the main model.
	if parent.Schemas()[0].Name != "run_code" || len(parent.Schemas()) != 1 {
		t.Fatalf("the parent's exclusive surface changed: %v", parent.Schemas())
	}
}

// Program tools call other tools by name through the parent's engine. A child
// that inherited them could run a program whose allowlist check happened in the
// parent's boundary, so no child registry carries them at all.
func TestSubagentRegistriesNeverCarryProgramTools(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(visTool{"run_code"})
	parent.Add(visTool{"run_script"})
	parent.Add(visTool{"read_file"})

	for _, names := range [][]string{nil, {"run_code", "run_script", "read_file"}} {
		for label, child := range map[string]*tool.Registry{
			"writer":    SubagentToolRegistry(parent, names),
			"read-only": ReadOnlySubagentToolRegistry(parent, names),
		} {
			for _, hidden := range []string{"run_code", "run_script"} {
				if _, ok := child.Get(hidden); ok {
					t.Errorf("%s child with allowlist %v holds %s", label, names, hidden)
				}
			}
			if _, ok := child.Get("read_file"); !ok {
				t.Errorf("%s child with allowlist %v lost read_file", label, names)
			}
		}
	}
}

// Strict read-only children keep their own read-only construction and never
// mirror the parent's exclusive pin, which constrains only the main model.
func TestReadOnlySubagentRegistryDoesNotInheritTheExclusivePin(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(visTool{"read_file"})
	parent.Add(visTool{"grep"})
	parent.Add(visTool{"run_code"})
	parent.SetProviderVisibleToolsExclusive([]string{"run_code"})

	child := ReadOnlySubagentToolRegistry(parent, nil)
	want := []string{"grep", "read_file"}
	if got := childSchemaNames(child); !slices.Equal(got, want) {
		t.Fatalf("read-only child schema = %v, want %v", got, want)
	}
}
