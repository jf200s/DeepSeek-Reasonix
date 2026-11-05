// Package ptc implements programmatic tool calling: the model writes a short
// JavaScript program that calls tools, loops over their results and logs what
// it needs, so several dependent steps cost one round trip and only the log
// output and return value enter the conversation.
//
// Every call inside a program goes through the agent's own invoker, so it is
// approved, hooked and observed exactly as if the model had made it. The
// program itself reaches nothing outside: no files, no network and no modules,
// only the tool bindings, log/console.log and the standard JavaScript built-ins
// (Date and Math included, so a real clock and random numbers are available).
package ptc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"

	"reasonix/internal/contract/tool"
)

// Tool is one callable tool as the program sees it.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	ReadOnly    bool
}

// Limits bounds one program run. Zero values take the defaults.
type Limits struct {
	Timeout         time.Duration // default 120s, max 600s
	MaxCalls        int           // default 100
	MaxProgramBytes int           // default 64*1024
	MaxOutputBytes  int           // default 64*1024
	MaxParallel     int           // default 8, max 16
}

const (
	defaultTimeout     = 120 * time.Second
	maxTimeout         = 600 * time.Second
	defaultMaxCalls    = 100
	defaultProgramSize = 64 * 1024
	defaultOutputSize  = 64 * 1024
	defaultMaxParallel = 8
	maxParallel        = 16
	// interruptGrace bounds how long a stopped run waits for the interpreter to
	// acknowledge an interrupt. A host call that ignores its context must not
	// hold the program, and the turn, past the time limit.
	interruptGrace = 50 * time.Millisecond
	// maxCallStackSize keeps runaway recursion an ordinary JS error instead of a
	// Go stack overflow.
	maxCallStackSize = 10_000
)

// DefaultLimits returns the limits a run gets when none are configured.
func DefaultLimits() Limits {
	return Limits{
		Timeout:         defaultTimeout,
		MaxCalls:        defaultMaxCalls,
		MaxProgramBytes: defaultProgramSize,
		MaxOutputBytes:  defaultOutputSize,
		MaxParallel:     defaultMaxParallel,
	}
}

// Normalized replaces zero values with defaults and clamps the maxima.
func (l Limits) Normalized() Limits {
	d := DefaultLimits()
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	if l.Timeout > maxTimeout {
		l.Timeout = maxTimeout
	}
	if l.MaxCalls <= 0 {
		l.MaxCalls = d.MaxCalls
	}
	if l.MaxProgramBytes <= 0 {
		l.MaxProgramBytes = d.MaxProgramBytes
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = d.MaxOutputBytes
	}
	if l.MaxParallel <= 0 {
		l.MaxParallel = d.MaxParallel
	}
	if l.MaxParallel > maxParallel {
		l.MaxParallel = maxParallel
	}
	return l
}

var (
	// ErrProgramFailed is a program that stopped on an error. The result still
	// carries what it logged first.
	ErrProgramFailed = errors.New("program failed")
	// ErrNoInvoker is a program run where no agent offers tool calls.
	ErrNoInvoker = errors.New("run_code: no tool invoker is bound to this call")
	// ErrNested is run_code called from inside a program.
	ErrNested = errors.New("run_code cannot be called from inside a program")
)

// awaitPattern finds an await expression. run_code is synchronous by design,
// and a program that waits for a promise it can never resolve would otherwise
// time out instead of telling the model what to fix.
var awaitPattern = regexp.MustCompile(`(?m)(^|[^\w.])await\s+`)

// Engine runs programs against a fixed tool set.
type Engine struct {
	tools  []Tool
	byName map[string]Tool
	limits Limits
}

// NewEngine returns an engine for tools. Blank and duplicate names are dropped
// (the first occurrence wins) so the contract stays deterministic.
func NewEngine(tools []Tool, limits Limits) *Engine {
	seen := make(map[string]bool, len(tools))
	kept := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == "" || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		kept = append(kept, t)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	byName := make(map[string]Tool, len(kept))
	for _, t := range kept {
		byName[t.Name] = t
	}
	return &Engine{tools: kept, byName: byName, limits: limits.Normalized()}
}

// Run executes program (a JavaScript function body) and returns the text the
// model reads: captured log lines, the return value, then a summary line. On
// failure it returns the partial output together with an error wrapping
// ErrProgramFailed.
func (e *Engine) Run(ctx context.Context, program string) (string, error) {
	if strings.TrimSpace(program) == "" {
		return "", fmt.Errorf("%w: the program is empty", ErrProgramFailed)
	}
	if len(program) > e.limits.MaxProgramBytes {
		return "", fmt.Errorf("%w: the program is over %d bytes", ErrProgramFailed, e.limits.MaxProgramBytes)
	}
	if tool.IsNested(ctx) {
		return "", ErrNested
	}
	inv, ok := tool.InvokerFrom(ctx)
	if !ok {
		return "", ErrNoInvoker
	}
	if awaitPattern.MatchString(program) {
		return "", fmt.Errorf("%w: run_code is synchronous and cannot await; remove `await` (and Promises) and call tools directly", ErrProgramFailed)
	}
	runCtx, cancel := context.WithTimeout(ctx, e.limits.Timeout)
	defer cancel()
	r := &programRun{
		inv:    inv,
		ctx:    runCtx,
		parent: ctx,
		cancel: cancel,
		limits: e.limits,
		byName: e.byName,
		counts: map[string]int{},
	}
	val, err := r.exec(program)
	out := r.result(val)
	var stop *stopError
	if errors.As(err, &stop) && stop.stillRunning {
		out = appendNotice(out, stillRunningNotice)
	}
	if err != nil {
		return out, fmt.Errorf("%w: %s", ErrProgramFailed, err)
	}
	return out, nil
}

// stillRunningNotice tells the model that stopping the program did not stop a
// host call it had already started: the call's context was cancelled, but a
// tool that ignores it may still finish and leave its effects behind.
const stillRunningNotice = "[a tool call was still running when the program stopped; its side effects may still land]"

// stopError is a run stopped from outside — its own time limit or a cancelled
// caller. stillRunning says whether the interpreter acknowledged the interrupt
// within interruptGrace; when it did not, a host call may outlive the program.
type stopError struct {
	reason       string
	stillRunning bool
}

func (e *stopError) Error() string {
	if e.stillRunning {
		return e.reason + "\n" + stillRunningNotice
	}
	return e.reason
}

// appendNotice adds one standalone line to the result text.
func appendNotice(out, notice string) string {
	if out == "" {
		return notice + "\n"
	}
	return strings.TrimRight(out, "\n") + "\n" + notice + "\n"
}

// programRun is one execution: what it logged and which tools it called.
type programRun struct {
	vm     *goja.Runtime
	inv    tool.Invoker
	ctx    context.Context
	parent context.Context
	cancel context.CancelFunc
	limits Limits
	byName map[string]Tool

	mu      sync.Mutex
	out     strings.Builder
	clipped bool
	calls   int
	counts  map[string]int
}

func (r *programRun) exec(program string) (goja.Value, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(maxCallStackSize)
	r.vm = vm
	r.install()

	src := "(function(){\n" + program + "\n})()"
	type outcome struct {
		val goja.Value
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		var out outcome
		defer func() {
			if p := recover(); p != nil {
				out = outcome{err: fmt.Errorf("%v", p)}
			}
		}()
		out.val, out.err = vm.RunString(src)
		done <- out
	}()

	select {
	case out := <-done:
		return out.val, out.err
	case <-r.ctx.Done():
		// The derived context is the program's own timer, but it also ends when
		// the caller's context does. Only an expiry that happened while the
		// parent was still live is the program's own timeout; a parent deadline
		// that fired first is a cancellation of the whole run.
		timedOut := errors.Is(r.ctx.Err(), context.DeadlineExceeded) && r.parent.Err() == nil
		interrupt := "the run was cancelled"
		reason := interrupt
		if timedOut {
			interrupt = "the program exceeded its time limit"
			reason = fmt.Sprintf("the program exceeded its %s time limit", r.limits.Timeout)
		}
		// Cancel first so a host call blocked on the context can return, then
		// interrupt the interpreter. Either way the wait is bounded: a tool that
		// ignores its context must not keep the program alive past the limit.
		r.cancel()
		vm.Interrupt(interrupt)
		stillRunning := false
		select {
		case <-done:
		case <-time.After(interruptGrace):
			stillRunning = true
		}
		return nil, &stopError{reason: reason, stillRunning: stillRunning}
	}
}

func (r *programRun) install() {
	vm := r.vm
	tools := vm.NewObject()
	for _, t := range r.tools() {
		name := t.Name
		_ = tools.Set(name, r.wrap(name, func(call goja.FunctionCall) (goja.Value, error) {
			out, err := r.invoke(name, call.Argument(0))
			if err != nil {
				return nil, err
			}
			return vm.ToValue(out), nil
		}))
	}
	_ = tools.Set("tryCall", r.wrap("tryCall", r.tryCall))
	_ = tools.Set("parallel", r.wrap("parallel", r.parallel))
	_ = vm.Set("tools", tools)

	logFn := r.wrap("log", func(call goja.FunctionCall) (goja.Value, error) {
		parts := make([]string, len(call.Arguments))
		for i, arg := range call.Arguments {
			parts[i] = r.stringify(arg)
		}
		r.writeLine(parts)
		return goja.Undefined(), nil
	})
	_ = vm.Set("log", logFn)
	console := vm.NewObject()
	_ = console.Set("log", logFn)
	_ = vm.Set("console", console)
}

func (r *programRun) tools() []Tool {
	out := make([]Tool, 0, len(r.byName))
	for name := range r.byName {
		out = append(out, r.byName[name])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// wrap converts a Go error (or panic) from a binding into a JS error, so a
// failing host call stops the program the same way a JS throw would.
func (r *programRun) wrap(name string, fn func(goja.FunctionCall) (goja.Value, error)) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		v, err := func() (v goja.Value, err error) {
			defer func() {
				if p := recover(); p != nil {
					if e, ok := p.(error); ok {
						err = e
						return
					}
					err = fmt.Errorf("%s: %v", name, p)
				}
			}()
			return fn(call)
		}()
		if err != nil {
			panic(r.vm.NewGoError(err))
		}
		return v
	}
}

// errCallLimit marks the call-budget failure. tryCall and parallel rethrow it
// rather than reporting it as an ordinary tool failure: the budget bounds the
// whole program, so it must stop it, exactly as the sync form does.
var errCallLimit = errors.New("tool call limit reached")

func (r *programRun) tryCall(call goja.FunctionCall) (goja.Value, error) {
	name, err := r.toolName(call.Argument(0))
	if err != nil {
		return nil, err
	}
	// Only declared tools have a binding, exactly as in parallel; refusing an
	// unknown name here keeps tryCall from promising a result the engine can
	// never produce.
	if _, ok := r.byName[name]; !ok {
		obj := r.vm.NewObject()
		_ = obj.Set("ok", false)
		_ = obj.Set("output", "")
		_ = obj.Set("error", "unknown tool")
		return obj, nil
	}
	out, callErr := r.invoke(name, call.Argument(1))
	if errors.Is(callErr, errCallLimit) {
		return nil, callErr
	}
	obj := r.vm.NewObject()
	if callErr != nil {
		_ = obj.Set("ok", false)
		_ = obj.Set("output", out)
		_ = obj.Set("error", callErr.Error())
		return obj, nil
	}
	_ = obj.Set("ok", true)
	_ = obj.Set("output", out)
	_ = obj.Set("error", "")
	return obj, nil
}

func (r *programRun) parallel(call goja.FunctionCall) (goja.Value, error) {
	list := call.Argument(0)
	obj, ok := list.(*goja.Object)
	if !ok || obj.ClassName() != "Array" {
		return nil, errors.New("parallel: the first argument must be an array of {name, args} calls")
	}
	n := int(obj.Get("length").ToInteger())
	if n < 0 {
		n = 0
	}
	// The call budget bounds the whole program; refusing an oversized list up
	// front keeps a hostile program from making the engine allocate for calls
	// that could never run.
	if n > r.limits.MaxCalls {
		return nil, fmt.Errorf("parallel: %d calls exceed the program's limit of %d tool calls", n, r.limits.MaxCalls)
	}

	calls := make([]parallelCall, n)
	results := make([]parallelResult, n)
	for i := 0; i < n; i++ {
		entry := obj.GetIdx(int64(i))
		entryObj, ok := entry.(*goja.Object)
		if !ok {
			results[i] = parallelResult{errText: fmt.Sprintf("parallel[%d]: each call must be an object with a name", i)}
			continue
		}
		name, err := r.toolName(entryObj.Get("name"))
		if err != nil {
			results[i] = parallelResult{errText: fmt.Sprintf("parallel[%d]: %v", i, err)}
			continue
		}
		t, ok := r.byName[name]
		if !ok {
			results[i] = parallelResult{errText: fmt.Sprintf("parallel[%d]: unknown tool %q", i, name)}
			continue
		}
		if !t.ReadOnly {
			results[i] = parallelResult{errText: "parallel is read-only; call writers sequentially"}
			continue
		}
		args, err := r.arguments(entryObj.Get("args"))
		if err != nil {
			results[i] = parallelResult{errText: fmt.Sprintf("parallel[%d]: %v", i, err)}
			continue
		}
		calls[i] = parallelCall{name: name, args: args, run: true}
	}

	conc := n
	if v := call.Argument(1); !goja.IsUndefined(v) && !goja.IsNull(v) {
		conc = int(v.ToInteger())
		if conc <= 0 {
			conc = n
		}
	}
	if conc > r.limits.MaxParallel {
		conc = r.limits.MaxParallel
	}
	if conc > n {
		conc = n
	}

	if conc > 0 {
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		for i := range calls {
			if !calls[i].run {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					if p := recover(); p != nil {
						results[i] = parallelResult{errText: fmt.Sprintf("parallel[%d]: %v", i, p)}
					}
				}()
				out, err := r.invokeArgs(calls[i].name, calls[i].args)
				if err != nil {
					// The call budget bounds the whole program, so it stops the
					// program here instead of becoming one item's error.
					if errors.Is(err, errCallLimit) {
						results[i] = parallelResult{fatal: err}
						return
					}
					results[i] = parallelResult{output: out, errText: err.Error()}
					return
				}
				results[i] = parallelResult{ok: true, output: out}
			}(i)
		}
		wg.Wait()
	}
	for i := range results {
		if results[i].fatal != nil {
			return nil, results[i].fatal
		}
	}

	values := make([]interface{}, len(results))
	for i, res := range results {
		values[i] = map[string]interface{}{"ok": res.ok, "output": res.output, "error": res.errText}
	}
	return r.vm.NewArray(values...), nil
}

type parallelCall struct {
	name string
	args json.RawMessage
	run  bool
}

type parallelResult struct {
	ok      bool
	output  string
	errText string
	fatal   error // a whole-program failure, rethrown after the batch
}

func (r *programRun) toolName(v goja.Value) (string, error) {
	if v == nil || !goja.IsString(v) {
		return "", errors.New("the tool name must be a string")
	}
	name := strings.TrimSpace(v.String())
	if name == "" {
		return "", errors.New("the tool name must not be empty")
	}
	return name, nil
}

func (r *programRun) invoke(name string, arg goja.Value) (string, error) {
	args, err := r.arguments(arg)
	if err != nil {
		return "", err
	}
	return r.invokeArgs(name, args)
}

func (r *programRun) invokeArgs(name string, args json.RawMessage) (string, error) {
	if err := r.beginCall(name); err != nil {
		return "", err
	}
	return r.inv.Invoke(r.ctx, name, args)
}

func (r *programRun) beginCall(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls >= r.limits.MaxCalls {
		return fmt.Errorf("%w: the program reached its limit of %d tool calls", errCallLimit, r.limits.MaxCalls)
	}
	r.calls++
	r.counts[name]++
	return nil
}

// arguments encodes the JS argument object as JSON. A missing or null argument
// means an empty object, which is how every tool's schema is written.
func (r *programRun) arguments(v goja.Value) (json.RawMessage, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return json.RawMessage("{}"), nil
	}
	data, err := json.Marshal(v.Export())
	if err != nil {
		return nil, fmt.Errorf("arguments are not JSON: %w", err)
	}
	if string(data) == "null" {
		return json.RawMessage("{}"), nil
	}
	return data, nil
}

// stringify renders one log argument: strings as-is, objects and arrays as
// JSON, everything else by its JS text.
func (r *programRun) stringify(v goja.Value) string {
	switch {
	case v == nil || goja.IsUndefined(v):
		return "undefined"
	case goja.IsNull(v):
		return "null"
	case goja.IsString(v):
		return v.String()
	}
	if data, err := json.Marshal(v.Export()); err == nil {
		return string(data)
	}
	return v.String()
}

// writeLine appends one log line. It clips part by part, so a single call with
// huge arguments never has to join the whole line first.
func (r *programRun) writeLine(parts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, part := range parts {
		if i > 0 {
			r.writeLocked(" ")
			if r.clipped {
				return
			}
		}
		r.writeLocked(part)
		if r.clipped {
			return
		}
	}
	r.writeLocked("\n")
}

func (r *programRun) writeLocked(s string) {
	if r.clipped {
		return
	}
	remaining := r.limits.MaxOutputBytes - r.out.Len()
	if len(s) <= remaining {
		r.out.WriteString(s)
		return
	}
	if remaining > 0 {
		cut := remaining
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		r.out.WriteString(s[:cut])
	}
	r.out.WriteString(r.clipMarker())
	r.out.WriteString("\n")
	r.clipped = true
}

// clipMarker names the budget once output is cut, so the model knows the value
// is incomplete rather than empty.
func (r *programRun) clipMarker() string {
	return fmt.Sprintf("[... output past %d bytes left out ...]", r.limits.MaxOutputBytes)
}

// result is what the program logged, then its return value, then one line
// naming the calls it made.
func (r *programRun) result(val goja.Value) string {
	r.mu.Lock()
	logged := r.out.String()
	calls := r.calls
	counts := make(map[string]int, len(r.counts))
	for name, n := range r.counts {
		counts[name] = n
	}
	r.mu.Unlock()

	var b strings.Builder
	b.WriteString(logged)
	if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
		text := r.returnText(val, r.limits.MaxOutputBytes-b.Len())
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		b.WriteString("(the program returned nothing)\n")
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s ×%d", name, counts[name]))
	}
	fmt.Fprintf(&b, "--- %d tool call(s)", calls)
	if len(parts) > 0 {
		b.WriteString(": " + strings.Join(parts, ", "))
	}
	return b.String()
}

// returnText renders the return value within budget bytes, marking a cut the
// way log output does. budget is what the logs left of MaxOutputBytes.
func (r *programRun) returnText(v goja.Value, budget int) string {
	var text string
	if goja.IsString(v) {
		text = v.String()
	} else if data, err := json.Marshal(v.Export()); err == nil {
		text = string(data)
	} else {
		text = v.String()
	}
	if text == "" {
		return ""
	}
	if len(text) <= budget {
		return text
	}
	if budget <= 0 {
		return r.clipMarker()
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + r.clipMarker()
}
