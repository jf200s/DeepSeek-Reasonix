package ptc

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestContractGolden(t *testing.T) {
	tools := []Tool{
		{
			Name:        "bash",
			Description: "Run a shell command in the workspace. Returns stdout and stderr.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"number"}},"required":["command"]}`),
		},
		{
			Name:        "read_file",
			Description: "Read a text file.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		},
		{
			Name:        "multi_edit",
			Description: "Apply edits to one file.\nEdits are applied in order.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"string"}},"mode":{"enum":["replace","append"]}},"required":["path","edits"]}`),
		},
	}
	want := contractHeader +
		"tools.bash({command: string; timeout_seconds?: number}) -> string // Run a shell command in the workspace.\n" +
		"tools.multi_edit({edits: string[]; path: string; mode?: \"replace\" | \"append\"}) -> string // Apply edits to one file.\n" +
		"tools.read_file({path: string}) -> string // Read a text file.\n" +
		contractHelpers
	if got := Contract(tools); got != want {
		t.Fatalf("Contract:\n%s\nwant:\n%s", got, want)
	}
}

func TestContractIsDeterministicRegardlessOfInputOrder(t *testing.T) {
	a := []Tool{
		{Name: "b", Schema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)},
		{Name: "a", Description: "Does A. More.", Schema: json.RawMessage(`{"type":"object","properties":{"y":{"type":"boolean"}}}`)},
	}
	b := []Tool{a[1], a[0]}
	if Contract(a) != Contract(b) {
		t.Fatalf("input order changed the contract:\n%s\n---\n%s", Contract(a), Contract(b))
	}
	if strings.Index(Contract(a), "tools.a(") > strings.Index(Contract(a), "tools.b(") {
		t.Fatalf("tools are not sorted:\n%s", Contract(a))
	}
}

func TestContractEmptyToolSetDocumentsHelpersOnly(t *testing.T) {
	got := Contract(nil)
	want := contractHeader + contractHelpers
	if got != want {
		t.Fatalf("Contract(nil) = %q, want %q", got, want)
	}
	if !strings.Contains(got, "tools.tryCall") || !strings.Contains(got, "tools.parallel") {
		t.Fatalf("helpers missing:\n%s", got)
	}
}

// generatedTools builds n deterministic tools with params required string
// properties each. desc, when non-empty, becomes every description.
func generatedTools(n, params int, desc string) []Tool {
	tools := make([]Tool, n)
	for i := range tools {
		props := make(map[string]json.RawMessage, params)
		required := make([]string, 0, params)
		for p := 0; p < params; p++ {
			name := fmt.Sprintf("param_%02d", p)
			props[name] = json.RawMessage(`{"type":"string"}`)
			required = append(required, name)
		}
		schema, err := json.Marshal(map[string]any{
			"type":       "object",
			"properties": props,
			"required":   required,
		})
		if err != nil {
			panic(err)
		}
		tools[i] = Tool{
			Name:        fmt.Sprintf("tool_%03d", i),
			Description: desc,
			Schema:      schema,
			ReadOnly:    true,
		}
	}
	return tools
}

func TestContractDropsDescriptionsFirst(t *testing.T) {
	got := Contract(generatedTools(200, 2, strings.Repeat("A detailed explanation of this tool ", 6)+"for the model."))
	if len(got) > contractBudget {
		t.Fatalf("len = %d, want <= %d", len(got), contractBudget)
	}
	if strings.Contains(got, " // ") {
		t.Fatalf("descriptions survived an over-budget full contract:\n%.400s", got)
	}
	if !strings.Contains(got, "tools.tool_000({param_00: string; param_01: string}) -> string") {
		t.Fatalf("signatures missing:\n%.400s", got)
	}
}

func TestContractFallsBackToRequiredNames(t *testing.T) {
	got := Contract(generatedTools(200, 8, ""))
	if len(got) > contractBudget {
		t.Fatalf("len = %d, want <= %d", len(got), contractBudget)
	}
	if !strings.Contains(got, "tools.tool_000(param_00, param_01, ") {
		t.Fatalf("required names missing:\n%.400s", got)
	}
	if strings.Contains(got, "param_00: string") {
		t.Fatalf("types survived the required-names fallback:\n%.400s", got)
	}
}

func TestContractFallsBackToNamesOnly(t *testing.T) {
	got := Contract(generatedTools(400, 8, ""))
	if len(got) > contractBudget {
		t.Fatalf("len = %d, want <= %d", len(got), contractBudget)
	}
	if !strings.Contains(got, "\ntools.tool_000\n") {
		t.Fatalf("names-only line missing:\n%.400s", got)
	}
	if strings.Contains(got, "tools.tool_000(") {
		t.Fatalf("signature survived the names-only fallback:\n%.400s", got)
	}
}

func TestContractNeverExceedsBudget(t *testing.T) {
	got := Contract(generatedTools(4000, 2, "A tool."))
	if len(got) > contractBudget {
		t.Fatalf("len = %d, want <= %d", len(got), contractBudget)
	}
	if !strings.Contains(got, "more tool(s) omitted") {
		t.Fatalf("truncated contract does not say what it omitted:\n%.400s", got)
	}
	if !strings.Contains(got, "tools.tool_000\n") {
		t.Fatalf("truncated contract kept no tools:\n%.400s", got)
	}
}

func TestContractStatesPromiseAndProxyRules(t *testing.T) {
	got := Contract(nil)
	for _, want := range []string{"Promise callbacks run before the program returns", "call writers and proxy tools sequentially"} {
		if !strings.Contains(got, want) {
			t.Fatalf("contract does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Promises)") {
		t.Fatalf("contract still claims Promises are refused:\n%s", got)
	}
}

func TestContractRendersUnknownAndDeepTypes(t *testing.T) {
	tools := []Tool{{
		Name: "odd",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"missing":{},
			"deep":{"type":"array","items":{"type":"array","items":{"type":"array","items":{"type":"string"}}}},
			"flag":{"type":"boolean"},
			"count":{"type":"integer"},
			"blob":{"type":"object"}
		},"required":["missing"]}`),
	}}
	got := Contract(tools)
	want := "tools.odd({missing: unknown; blob?: object; count?: integer; deep?: unknown[][]; flag?: boolean}) -> string"
	if !strings.Contains(got, want) {
		t.Fatalf("got:\n%s\nwant a line containing:\n%s", got, want)
	}
}

func TestContractAccessorForNonIdentifierNames(t *testing.T) {
	tools := []Tool{{Name: "mcp-server.tool", Schema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)}}
	got := Contract(tools)
	if !strings.Contains(got, `tools["mcp-server.tool"]({x?: string}) -> string`) {
		t.Fatalf("got:\n%s", got)
	}
}
