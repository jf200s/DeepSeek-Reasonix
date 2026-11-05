package ptc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/contract/tool"
)

// fakeInvoker answers read_file with the path it was asked for, fails bash,
// succeeds write_file, and records calls plus peak concurrency.
type fakeInvoker struct {
	mu          sync.Mutex
	calls       []string
	inFlight    int
	maxInFlight int
	delay       time.Duration
	panicOn     string
}

func (f *fakeInvoker) Invoke(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name+" "+string(args))
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()

	if f.delay > 0 {
		time.Sleep(f.delay)
	}

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()

	if f.panicOn != "" && name == f.panicOn {
		panic("boom")
	}
	switch name {
	case "read_file":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(args, &p)
		return "contents of " + p.Path, nil
	case "bash":
		return "exit status 1", fmt.Errorf("%w: bash: exit status 1", tool.ErrNestedCallFailed)
	case "write_file":
		return "wrote", nil
	default:
		return "ok", nil
	}
}

func (f *fakeInvoker) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeInvoker) peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInFlight
}

func runEngine(t *testing.T, e *Engine, inv tool.Invoker, program string) (string, error) {
	t.Helper()
	ctx := t.Context()
	if inv != nil {
		ctx = tool.WithInvoker(ctx, inv)
	}
	return e.Run(ctx, program)
}

func TestRunCallsToolsSynchronously(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, DefaultLimits())
	out, err := runEngine(t, e, inv, `
const a = tools.read_file({path: "a.go"});
const b = tools.read_file({path: "b.go"});
log(a);
log(b);
return a + "|" + b;
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "contents of a.go\ncontents of b.go\ncontents of a.go|contents of b.go\n--- 2 tool call(s): read_file ×2"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	calls := inv.recorded()
	if len(calls) != 2 || calls[0] != `read_file {"path":"a.go"}` || calls[1] != `read_file {"path":"b.go"}` {
		t.Fatalf("calls = %q", calls)
	}
}

func TestTryCallReturnsAStructInsteadOfThrowing(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{
		{Name: "read_file", ReadOnly: true},
		{Name: "bash"},
	}, DefaultLimits())
	out, err := runEngine(t, e, inv, `
const ok = tools.tryCall("read_file", {path: "x"});
const bad = tools.tryCall("bash", {command: "false"});
log(ok.ok, ok.output, ok.error || "-");
log(bad.ok, bad.output, bad.error);
return bad.ok;
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "true contents of x -\nfalse exit status 1 tool call failed: bash: exit status 1\nfalse\n--- 2 tool call(s): bash ×1, read_file ×1"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestThrownHostErrorStopsTheProgramAndKeepsPartialOutput(t *testing.T) {
	e := NewEngine([]Tool{{Name: "bash"}}, DefaultLimits())
	out, err := runEngine(t, e, &fakeInvoker{}, `
log("before");
tools.bash({command: "false"});
log("after");
`)
	if !errors.Is(err, ErrProgramFailed) {
		t.Fatalf("err = %v, want ErrProgramFailed", err)
	}
	if !strings.Contains(err.Error(), "bash: exit status 1") {
		t.Fatalf("err = %v, want the host error", err)
	}
	if !strings.HasPrefix(out, "before\n") || strings.Contains(out, "after") {
		t.Fatalf("out = %q", out)
	}
	if !strings.HasSuffix(out, "--- 1 tool call(s): bash ×1") {
		t.Fatalf("out = %q, want the summary", out)
	}
}

func TestParallelPreservesOrderAndRefusesWriters(t *testing.T) {
	inv := &fakeInvoker{delay: 20 * time.Millisecond}
	e := NewEngine([]Tool{
		{Name: "read_file", ReadOnly: true},
		{Name: "write_file"},
	}, Limits{MaxParallel: 2})
	out, err := runEngine(t, e, inv, `
const r = tools.parallel([
  {name: "read_file", args: {path: "a"}},
  {name: "write_file", args: {path: "b"}},
  {name: "read_file", args: {path: "c"}}
], 2);
for (const x of r) log(x.ok, x.output || "-", x.error || "-");
return r.length;
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "true contents of a -\n" +
		"false - parallel is read-only; call writers sequentially\n" +
		"true contents of c -\n" +
		"3\n" +
		"--- 2 tool call(s): read_file ×2"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if calls := inv.recorded(); len(calls) != 2 {
		t.Fatalf("calls = %q; a writer must not be invoked", calls)
	}
	if peak := inv.peak(); peak < 2 {
		t.Fatalf("peak concurrency = %d, want at least 2", peak)
	}
}

func TestParallelHonorsMaxParallel(t *testing.T) {
	inv := &fakeInvoker{delay: 20 * time.Millisecond}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, Limits{MaxParallel: 2})
	out, err := runEngine(t, e, inv, `
const calls = [];
for (let i = 0; i < 6; i++) calls.push({name: "read_file", args: {path: String(i)}});
const r = tools.parallel(calls, 16);
return r.filter(x => x.ok).length;
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(out, "6\n") {
		t.Fatalf("out = %q, want 6 results", out)
	}
	if peak := inv.peak(); peak > 2 {
		t.Fatalf("peak concurrency = %d, want at most 2", peak)
	}
	if len(inv.recorded()) != 6 {
		t.Fatalf("calls = %q", inv.recorded())
	}
}

func TestCallLimit(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, Limits{MaxCalls: 2})
	out, err := runEngine(t, e, inv, `
for (let i = 0; i < 5; i++) tools.read_file({path: String(i)});
`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "limit of 2 tool calls") {
		t.Fatalf("err = %v", err)
	}
	if len(inv.recorded()) != 2 {
		t.Fatalf("calls = %q, want 2", inv.recorded())
	}
	if !strings.HasSuffix(out, "--- 2 tool call(s): read_file ×2") {
		t.Fatalf("out = %q", out)
	}
}

func TestTryCallStillStopsAtTheCallLimit(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, Limits{MaxCalls: 1})
	_, err := runEngine(t, e, inv, `
tools.read_file({path: "a"});
tools.tryCall("read_file", {path: "b"});
`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "limit of 1 tool calls") {
		t.Fatalf("err = %v; the call budget must stop the program, not become a tryCall result", err)
	}
	if len(inv.recorded()) != 1 {
		t.Fatalf("calls = %q, want 1", inv.recorded())
	}
}

func TestTryCallRefusesAToolTheEngineDoesNotDeclare(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, DefaultLimits())
	out, err := runEngine(t, e, inv, `
const r = tools.tryCall("not_bound", {path: "x"});
log(r.ok, r.error);
return r.ok;
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "false unknown tool\nfalse\n--- 0 tool call(s)"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if len(inv.recorded()) != 0 {
		t.Fatalf("calls = %q; an undeclared name must not reach the invoker", inv.recorded())
	}
}

func TestParallelStopsAtTheCallLimit(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, Limits{MaxCalls: 2})
	_, err := runEngine(t, e, inv, `
tools.read_file({path: "a"});
tools.parallel([
  {name: "read_file", args: {path: "b"}},
  {name: "read_file", args: {path: "c"}}
]);
`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "limit of 2 tool calls") {
		t.Fatalf("err = %v; the call budget must stop the program, not become one parallel result", err)
	}
	if len(inv.recorded()) != 2 {
		t.Fatalf("calls = %q, want 2", inv.recorded())
	}
}

func TestReturnValueIsBoundedByTheOutputLimit(t *testing.T) {
	const limit = 16
	e := NewEngine(nil, Limits{MaxOutputBytes: limit})
	out, err := runEngine(t, e, &fakeInvoker{}, `return "x".repeat(5000);`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// MaxOutputBytes bounds the program's own output (logs and return value);
	// the marker and the call summary are engine metadata appended after it.
	body, _, clipped := strings.Cut(out, "[... output past")
	if !clipped {
		t.Fatalf("out = %q, want a clipping marker", out)
	}
	if len(body) > limit {
		t.Fatalf("program output = %d bytes, want <= %d", len(body), limit)
	}
	if strings.Contains(out, strings.Repeat("x", limit+1)) {
		t.Fatalf("out = %q, want the 5000-byte return value truncated", out)
	}
}

func TestLogOutputIsBounded(t *testing.T) {
	e := NewEngine(nil, Limits{MaxOutputBytes: 24})
	out, err := runEngine(t, e, &fakeInvoker{}, `
log("0123456789");
log("abcdefghij");
log("klmnopqrst");
`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "[... output past 24 bytes left out ...]") {
		t.Fatalf("out = %q, want a clipping marker", out)
	}
	if strings.Contains(out, "mnopqrst") {
		t.Fatalf("out = %q, want the tail clipped", out)
	}
	if !strings.HasSuffix(out, "--- 0 tool call(s)") {
		t.Fatalf("out = %q, want the summary", out)
	}
}

func TestReturnValueFormatting(t *testing.T) {
	e := NewEngine(nil, DefaultLimits())
	cases := []struct {
		program string
		want    string
	}{
		{`return "done";`, "done\n--- 0 tool call(s)"},
		{`return {z: 1, a: [true, "x"]};`, "{\"a\":[true,\"x\"],\"z\":1}\n--- 0 tool call(s)"},
		{`let x = 1;`, "(the program returned nothing)\n--- 0 tool call(s)"},
		{`return null;`, "(the program returned nothing)\n--- 0 tool call(s)"},
	}
	for _, tc := range cases {
		out, err := runEngine(t, e, &fakeInvoker{}, tc.program)
		if err != nil {
			t.Fatalf("%s: %v", tc.program, err)
		}
		if out != tc.want {
			t.Fatalf("%s: out = %q, want %q", tc.program, out, tc.want)
		}
	}
}

func TestAwaitFailsFast(t *testing.T) {
	inv := &fakeInvoker{}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, DefaultLimits())
	_, err := runEngine(t, e, inv, `const x = await tools.read_file({path: "a"});`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "await") {
		t.Fatalf("err = %v, want an await hint", err)
	}
	if len(inv.recorded()) != 0 {
		t.Fatalf("calls = %q, want none", inv.recorded())
	}
}

func TestNestedAndMissingInvoker(t *testing.T) {
	e := NewEngine(nil, DefaultLimits())
	ctx := tool.MarkNested(tool.WithInvoker(t.Context(), &fakeInvoker{}))
	if _, err := e.Run(ctx, "return 1;"); !errors.Is(err, ErrNested) {
		t.Fatalf("err = %v, want ErrNested", err)
	}
	if _, err := e.Run(context.Background(), "return 1;"); !errors.Is(err, ErrNoInvoker) {
		t.Fatalf("err = %v, want ErrNoInvoker", err)
	}
}

func TestProgramSizeAndEmptyProgram(t *testing.T) {
	e := NewEngine(nil, Limits{MaxProgramBytes: 16})
	if _, err := e.Run(tool.WithInvoker(t.Context(), &fakeInvoker{}), strings.Repeat("x", 100)); !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "over 16 bytes") {
		t.Fatalf("err = %v, want a size error", err)
	}
	if _, err := e.Run(tool.WithInvoker(t.Context(), &fakeInvoker{}), "   \n\t"); !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want an empty-program error", err)
	}
}

func TestTimeoutInterruptsTheProgram(t *testing.T) {
	e := NewEngine(nil, Limits{Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err := runEngine(t, e, &fakeInvoker{}, `while (true) {}`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("err = %v, want a time-limit error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stopped after %v", elapsed)
	}
}

// A tool call blocks the JavaScript thread, so the limit must cancel the call's
// context and stop waiting shortly after the interrupt rather than outliving the
// slow tool.
func TestTimeoutBoundsAToolCallThatIgnoresCancellation(t *testing.T) {
	inv := &fakeInvoker{delay: 400 * time.Millisecond}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, Limits{Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err := runEngine(t, e, inv, `return tools.read_file({path: "slow"});`)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("err = %v, want a time-limit error", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("stopped after %v, before the 50ms limit", elapsed)
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("stopped after %v; the limit must not wait for the tool's 400ms", elapsed)
	}
}

// A tool that ignores its context outlives the stop. The model must hear that
// the call may still land, in the result text as well as the error, or it will
// assume the stopped program left nothing behind.
func TestTimeoutNoticeWhenAHostCallOutlivesTheStop(t *testing.T) {
	inv := &fakeInvoker{delay: 400 * time.Millisecond}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, Limits{Timeout: 50 * time.Millisecond})
	start := time.Now()
	out, err := runEngine(t, e, inv, `log("before"); return tools.read_file({path: "slow"});`)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "the program exceeded") || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("err = %v, want the program's own time-limit error", err)
	}
	if strings.Contains(err.Error(), "the run was cancelled") {
		t.Fatalf("err = %v, the program's own timer must not read as a caller cancellation", err)
	}
	if !strings.Contains(err.Error(), stillRunningNotice) || !strings.Contains(out, stillRunningNotice) {
		t.Fatalf("text %q / err %v must both say the host call may still land", out, err)
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("stopped after %v; the grace period must bound the wait", elapsed)
	}
}

// The caller's deadline is not the program's time limit: the program used
// little of its budget, so reporting a program timeout would blame the wrong
// side.
func TestParentDeadlineIsACancellationNotTheProgramsTimeout(t *testing.T) {
	inv := &fakeInvoker{delay: 400 * time.Millisecond}
	e := NewEngine([]Tool{{Name: "read_file", ReadOnly: true}}, DefaultLimits())
	ctx, cancel := context.WithTimeout(tool.WithInvoker(t.Context(), inv), 50*time.Millisecond)
	defer cancel()
	out, err := e.Run(ctx, `return tools.read_file({path: "slow"});`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "the run was cancelled") {
		t.Fatalf("err = %v, want a cancellation error", err)
	}
	if strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("err = %v, a parent deadline must not be reported as the program's timeout", err)
	}
	if !strings.Contains(err.Error(), stillRunningNotice) || !strings.Contains(out, stillRunningNotice) {
		t.Fatalf("text %q / err %v must both say the host call may still land", out, err)
	}
}

func TestCancellationInterruptsTheProgram(t *testing.T) {
	ctx, cancel := context.WithCancel(tool.WithInvoker(t.Context(), &fakeInvoker{}))
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	e := NewEngine(nil, DefaultLimits())
	_, err := e.Run(ctx, `while (true) {}`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want a cancellation error", err)
	}
}

func TestPanicInHostCallBecomesAProgramError(t *testing.T) {
	e := NewEngine([]Tool{{Name: "bash"}}, DefaultLimits())
	out, err := runEngine(t, e, &fakeInvoker{panicOn: "bash"}, `
log("before");
tools.bash({command: "x"});
`)
	if !errors.Is(err, ErrProgramFailed) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(out, "before\n") {
		t.Fatalf("out = %q", out)
	}
}
