package runcode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/contract/tool"
	"reasonix/internal/tools/ptc"
)

type fakeInvoker struct{ calls []string }

func (f *fakeInvoker) Invoke(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.calls = append(f.calls, name+" "+string(args))
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &p)
	return "contents of " + p.Path, nil
}

func execute(t *testing.T, ctx context.Context, code string) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	return New([]ptc.Tool{{Name: "read_file", ReadOnly: true}}, ptc.DefaultLimits()).Execute(ctx, args)
}

func TestSchemaShape(t *testing.T) {
	tr := New(nil, ptc.DefaultLimits())
	if tr.Name() != Name || Name != "run_code" {
		t.Fatalf("name = %q", tr.Name())
	}
	if tr.ReadOnly() {
		t.Fatal("run_code can invoke writers, so it must not be ReadOnly")
	}
	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tr.Schema(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if schema.Type != "object" || len(schema.Required) != 1 || schema.Required[0] != "code" {
		t.Fatalf("schema = %+v", schema)
	}
	if schema.Properties["code"].Type != "string" {
		t.Fatalf("code property = %+v", schema.Properties["code"])
	}
	if tr.Description() == "" || !strings.Contains(tr.Description(), "run_code") {
		t.Fatalf("description = %q", tr.Description())
	}
}

func TestEmptyAndOversizedCode(t *testing.T) {
	ctx := tool.WithInvoker(t.Context(), &fakeInvoker{})
	if _, err := execute(t, ctx, "   \n\t"); err == nil || !strings.Contains(err.Error(), "code is required") {
		t.Fatalf("empty code: err = %v", err)
	}
	tr := New(nil, ptc.Limits{MaxProgramBytes: 16})
	args := json.RawMessage(`{"code":"` + strings.Repeat("x", 100) + `"}`)
	if _, err := tr.Execute(ctx, args); err == nil || !strings.Contains(err.Error(), "over 16 bytes") {
		t.Fatalf("oversized code: err = %v", err)
	}
}

func TestRefusesWithoutInvokerOrInsideAProgram(t *testing.T) {
	if _, err := execute(t, context.Background(), "return 1;"); !errors.Is(err, ptc.ErrNoInvoker) {
		t.Fatalf("err = %v, want ErrNoInvoker", err)
	}
	ctx := tool.MarkNested(tool.WithInvoker(t.Context(), &fakeInvoker{}))
	if _, err := execute(t, ctx, "return 1;"); !errors.Is(err, ptc.ErrNested) {
		t.Fatalf("err = %v, want ErrNested", err)
	}
}

func TestSuccessPath(t *testing.T) {
	inv := &fakeInvoker{}
	out, err := execute(t, tool.WithInvoker(t.Context(), inv), `return tools.read_file({path: "a.go"});`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "contents of a.go\n--- 1 tool call(s): read_file ×1"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if len(inv.calls) != 1 || inv.calls[0] != `read_file {"path":"a.go"}` {
		t.Fatalf("calls = %q", inv.calls)
	}
}
