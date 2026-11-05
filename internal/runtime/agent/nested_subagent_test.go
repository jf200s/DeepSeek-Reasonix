package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/contract/event"
	"reasonix/internal/contract/provider"
	"reasonix/internal/contract/tool"
	"reasonix/internal/state/sessionstore"
)

// nestedProbe reports the nested marker it was called under, so a test can say
// what a tool inside a delegated run really saw.
type nestedProbe struct {
	seen *bool
}

func (nestedProbe) Name() string            { return "nested_probe" }
func (nestedProbe) Description() string     { return "report the nested-call marker" }
func (nestedProbe) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (nestedProbe) ReadOnly() bool          { return true }

func (p nestedProbe) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	*p.seen = tool.IsNested(ctx)
	return fmt.Sprintf("nested=%t", *p.seen), nil
}

func TestWithoutNestedClearsTheMarkerOnTheDerivedContextOnly(t *testing.T) {
	nested := tool.MarkNested(context.Background())
	if !tool.IsNested(nested) {
		t.Fatal("MarkNested did not mark the context")
	}
	plain := tool.WithoutNested(nested)
	if tool.IsNested(plain) {
		t.Fatal("WithoutNested left the derived context marked nested")
	}
	if !tool.IsNested(nested) {
		t.Fatal("WithoutNested mutated its parent context")
	}
	if tool.IsNested(tool.WithoutNested(context.Background())) {
		t.Fatal("an unmarked context became marked")
	}
}

// A sub-agent spawned from inside a program runs as its own agent loop. Its
// model-issued calls are not nested program calls, so the degradation nested
// tools opt into — read_file returning verbatim text instead of paging — must
// stop at the child's entry.
func TestProgramSpawnedSubagentDoesNotInheritNestedDegradation(t *testing.T) {
	readFile, ok := tool.LookupBuiltin("read_file")
	if !ok {
		t.Fatal("read_file builtin is not registered")
	}
	path := filepath.Join(t.TempDir(), "large.txt")
	var body strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	var sawNested bool
	reg := tool.NewRegistry()
	reg.Add(nestedProbe{seen: &sawNested})
	reg.Add(readFile)

	prov := &scriptedProvider{name: "nested-subagent", turns: [][]provider.Chunk{
		{
			toolCallChunk("probe", "nested_probe", `{}`),
			toolCallChunk("read", "read_file", fmt.Sprintf(`{"path":%q,"limit":10}`, path)),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	sess := sessionstore.NewSession("CHILD-SYSTEM")
	ctx := tool.MarkNested(WithToolCallContext(context.Background(), "parent", event.Discard, nil, true))

	if _, err := RunSubAgentWithSession(ctx, prov, reg, sess, "inspect the file", Options{
		CheckTargetAccess: allowTestFileAccess,
	}, event.Discard); err != nil {
		t.Fatalf("RunSubAgentWithSession: %v", err)
	}
	if sawNested {
		t.Fatal("a tool inside the sub-agent still saw the program's nested marker")
	}
	got := lastToolResult(sess, "read_file")
	if !strings.Contains(got, "1→line 1") {
		t.Fatalf("read_file did not page inside the sub-agent; got %.200q", got)
	}
	if !strings.Contains(got, "more lines below") {
		t.Fatalf("read_file did not report pagination inside the sub-agent; got %.300q", got)
	}
}
