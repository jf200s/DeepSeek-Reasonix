// Package runcode is the run_code tool: the model writes a short JavaScript
// program that calls tools, loops over their results and logs what it needs, so
// several dependent steps cost one round trip and only the program's log output
// and return value enter the conversation. Every call inside the program goes
// through the agent's own invoker, so it is approved, hooked and observed
// exactly as if the model had made it; the interpreter can reach nothing else.
package runcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/contract/tool"
	"reasonix/internal/tools/ptc"
)

// Name is the tool name the model calls.
const Name = "run_code"

type runCode struct {
	engine *ptc.Engine
	limits ptc.Limits
}

// New returns the run_code tool over tools, bounded by limits.
func New(tools []ptc.Tool, limits ptc.Limits) tool.Tool {
	limits = limits.Normalized()
	return &runCode{engine: ptc.NewEngine(tools, limits), limits: limits}
}

func (*runCode) Name() string { return Name }

func (*runCode) Description() string {
	return "Run a short JavaScript program (the body of a synchronous function) that calls tools, so several dependent steps take one round trip and only what it logs or returns comes back. " +
		"Use it to read or search many files and keep only what matters, run a command and act on its output, or repeat one tool over a list. " +
		"In the program: tools.<name>(args) calls a tool synchronously and returns its output as a string, throwing if the call fails (read_file returns the file's exact text, ready to edit and pass to write_file); " +
		"tools.tryCall(name, args) returns {ok, output, error} instead of throwing; tools.parallel(calls, concurrency?) runs read-only tools concurrently, but not proxy tools, and returns {ok, output, error}[]; " +
		"log(...) and console.log(...) capture output and a returned value is included too. " +
		"Every call is checked and approved exactly as if you made it directly. The program has no file or network access of its own, " +
		"but Date and Math (including a real clock and random numbers) are available; it is synchronous (no await), and Promise callbacks still run before it returns. It cannot call run_code. " +
		"Prefer ordinary calls for one or two independent steps."
}

func (*runCode) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"JavaScript function body. Example: const src = tools.read_file({path: \"a.go\"}); log(src.length); return tools.bash({command: \"go test ./...\"})"}},"required":["code"],"additionalProperties":false}`)
}

func (*runCode) ReadOnly() bool { return false }

func (t *runCode) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.Code) == "" {
		return "", errors.New("invalid args: code is required")
	}
	if len(p.Code) > t.limits.MaxProgramBytes {
		return "", fmt.Errorf("invalid args: code is over %d bytes", t.limits.MaxProgramBytes)
	}
	return t.engine.Run(ctx, p.Code)
}
