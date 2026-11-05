package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/base/testenv"
	"reasonix/internal/contract/event"
	"reasonix/internal/contract/observe"
	"reasonix/internal/contract/provider"
	"reasonix/internal/contract/tool"
	"reasonix/internal/runtime/agent"
	"reasonix/internal/session/control"
	"reasonix/internal/tools/ptc"
	"reasonix/internal/tools/runcode"
	"reasonix/internal/tools/script"
)

// ptcReadTool is the one ordinary tool the exclusion test needs to exist.
type ptcReadTool struct{}

func (ptcReadTool) Name() string            { return "read_file" }
func (ptcReadTool) Description() string     { return "Read a text file." }
func (ptcReadTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (ptcReadTool) ReadOnly() bool          { return true }
func (ptcReadTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "read", nil
}

// A program cannot call another program runner: nested calls are always
// refused, so neither runner belongs in the contract a program is written
// against.
func TestPTCToolsOmitNestedRunners(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	reg.Add(ptcReadTool{})
	reg.Add(runcode.New(nil, ptc.Limits{}))
	reg.Add(script.New())
	got := ptcTools(reg, nil, nil)
	if len(got) != 1 || got[0].Name != "read_file" {
		t.Fatalf("ptcTools = %v, want only read_file", got)
	}
}

// A deferred MCP server's tools are registered for use_capability but are not
// on the provider surface. The contract must not name them either, or turning
// PTC on would quietly show a schema mcp_load=deferred kept hidden.
type ptcMCPTool struct{}

func (ptcMCPTool) Name() string            { return tool.MCPNamePrefix + "srv__call" }
func (ptcMCPTool) Description() string     { return "An MCP tool." }
func (ptcMCPTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (ptcMCPTool) ReadOnly() bool          { return true }
func (ptcMCPTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

func TestPTCToolsSkipMCPToolsNotAlwaysLoaded(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	reg.Add(ptcReadTool{})
	reg.Add(ptcMCPTool{})
	got := ptcTools(reg, nil, nil)
	if len(got) != 1 || got[0].Name != "read_file" {
		t.Fatalf("ptcTools = %v, want only read_file", got)
	}
	got = ptcTools(reg, nil, []string{tool.MCPNamePrefix + "srv__call"})
	if len(got) != 2 {
		t.Fatalf("ptcTools with an always-loaded server = %v, want both tools", got)
	}
}

// The contract is appended to a prompt that may already carry one, because a
// rebuild reuses the last system prompt. It must stay a single block, and a
// build that does not offer PTC must remove the old block.
func TestPTCContractIsAppendedOnceAndCanBeStripped(t *testing.T) {
	t.Parallel()
	once := appendPTCContract("BASE", "CONTRACT")
	if strings.Count(once, "CONTRACT") != 1 {
		t.Fatalf("contract appears %d times in %q", strings.Count(once, "CONTRACT"), once)
	}
	twice := appendPTCContract(once, "CONTRACT")
	if strings.Count(twice, "CONTRACT") != 1 {
		t.Fatalf("contract was appended twice: %q", twice)
	}
	if got := stripPTCContract(twice); got != "BASE" {
		t.Fatalf("strip = %q, want BASE", got)
	}
	if got := appendPTCContract(once, ""); got != "BASE" {
		t.Fatalf("append with no contract = %q, want BASE", got)
	}
}

// A configuration that enables PTC but disables run_code must fail the build:
// the session would otherwise advertise a program tool it no longer has, and in
// exclusive mode the model would see no tool it may call.
func TestBuildRefusesPTCWhenRunCodeIsDisabled(t *testing.T) {
	t.Parallel()
	home := statedBootHome(t)
	// The user's own list: this is an explicit contradiction between two
	// settings the same person wrote, so the build names it.
	writeFile(t, home, "config.toml", "[tools.ptc]\nenabled = true\n\n[tools]\ndisabled = [\"run_code\"]\n")
	dir := testenv.TempDir(t)
	kind := "boot-ptc-disabled-" + strings.ToLower(t.Name())
	provider.Register(kind, func(provider.Config) (provider.Provider, error) {
		return &browserScriptProvider{}, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "`+kind+`"
model = "x"
`)
	approveWorkspaceAt(t, home, dir)
	_, err := Build(context.Background(), Options{Home: home, WorkspaceRoot: dir, Sink: &noticeRecorder{}})
	if err == nil || !strings.Contains(err.Error(), "run_code") || !strings.Contains(err.Error(), "[tools].disabled") {
		t.Fatalf("Build = %v, want a refusal naming run_code and the list that removed it", err)
	}
}

// A project may remove run_code for its own sessions, but that is not the user
// asking PTC off: the build must proceed without PTC rather than fail, or a
// cloned repository could stop the user opening the workspace.
func TestBuildWithoutPTCWhenOnlyTheProjectDisablesRunCode(t *testing.T) {
	t.Parallel()
	home := statedBootHome(t)
	writeFile(t, home, "config.toml", "[tools.ptc]\nenabled = true\n")
	dir := testenv.TempDir(t)
	kind := "boot-ptc-project-disabled-" + strings.ToLower(t.Name())
	provider.Register(kind, func(provider.Config) (provider.Provider, error) {
		return &browserScriptProvider{}, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[tools]
disabled = ["run_code"]

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "`+kind+`"
model = "x"
`)
	approveWorkspaceAt(t, home, dir)
	res, err := BuildRuntime(context.Background(), Options{Home: home, WorkspaceRoot: dir, Sink: &noticeRecorder{}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer res.Controller.Close()
	prompt := ""
	if res.Snapshot != nil {
		prompt = res.Snapshot.SystemPrompt()
	}
	if strings.Contains(prompt, ptcContractStart) {
		t.Fatalf("a build without PTC kept the contract:\n%s", prompt)
	}
}

// A composition that selects its own tools never admits run_code, so PTC is
// simply unavailable there: the build must continue, and neither the system
// prompt nor the registry may advertise a program tool the model cannot call.
func TestBuildWithoutRunCodeInCompositionRunsWithoutPTC(t *testing.T) {
	t.Parallel()
	home, dir := compositionTestWorkspace(t, "openai")
	saveReadOnlyComposition(t, home, dir)
	writeFile(t, home, "config.toml", "[tools.ptc]\nenabled = true\n")
	res, err := BuildRuntime(context.Background(), Options{Home: home, WorkspaceRoot: dir, AgentComposition: "user:review", Sink: &noticeRecorder{}, resolvedShell: pinnedEffectShell()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer res.Controller.Close()
	if _, ok := res.Assembly.Registry.Get(runcode.Name); ok {
		t.Fatal("run_code is registered although the composition excludes it")
	}
	if strings.Contains(res.Assembly.SystemPrompt, "tools.tryCall(") {
		t.Fatalf("system prompt advertises a PTC contract with no run_code:\n%s", res.Assembly.SystemPrompt)
	}
}

// The read-only posture refuses run_code (it writes), so PTC is unavailable in
// an observe run for the same reason: Build continues without it.
func TestBuildUnderObserveRunsWithoutPTC(t *testing.T) {
	home := statedBootHome(t)
	writeFile(t, home, "config.toml", "[tools.ptc]\nenabled = true\n")
	root := testenv.TempDir(t)
	writeFile(t, root, "reasonix.toml", `
default_model = "test-model"

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
`)
	approveWorkspaceAt(t, home, root)
	res, err := BuildRuntime(context.Background(), Options{Home: home, Sink: event.Discard, WorkspaceRoot: root, Observe: &ObserveOptions{Pending: observe.NewLedger(nil)}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer res.Controller.Close()
	if _, ok := res.Assembly.Registry.Get(runcode.Name); ok {
		t.Fatal("run_code is registered under the read-only posture")
	}
	if strings.Contains(res.Assembly.SystemPrompt, "tools.tryCall(") {
		t.Fatalf("system prompt advertises a PTC contract with no run_code:\n%s", res.Assembly.SystemPrompt)
	}
}

// The unified surface drops every name it does not list, run_code included. In
// non-exclusive mode the program tool must come back beside the ordinary tools;
// in exclusive mode it is the whole surface.
func TestEffectPTCProviderSurfaceFollowsExclusiveMode(t *testing.T) {
	t.Parallel()
	for _, exclusive := range []bool{false, true} {
		t.Run(fmt.Sprintf("exclusive=%v", exclusive), func(t *testing.T) {
			t.Parallel()
			home := statedBootHome(t)
			writeFile(t, home, "config.toml", fmt.Sprintf("[tools.ptc]\nenabled = true\nexclusive = %v\n", exclusive))
			dir := testenv.TempDir(t)
			rec := &browserScriptProvider{}
			kind := "boot-ptc-surface-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
			provider.Register(kind, func(provider.Config) (provider.Provider, error) { return rec, nil })
			writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "`+kind+`"
model = "x"
`)
			approveWorkspaceAt(t, home, dir)
			ctrl, err := Build(context.Background(), Options{Home: home, WorkspaceRoot: dir, Sink: &noticeRecorder{}})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer ctrl.Close()
			if err := ctrl.Run(context.Background(), "hi"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			reqs := agentRequests(rec.requests())
			if len(reqs) == 0 {
				t.Fatal("the executor never sampled")
			}
			names := toolSchemaNames(reqs[0].Tools)
			if exclusive {
				if len(names) != 1 || names[0] != runcode.Name {
					t.Fatalf("exclusive provider tools = %v, want only %s", names, runcode.Name)
				}
				return
			}
			if !slices.Contains(names, runcode.Name) || !slices.Contains(names, "read_file") {
				t.Fatalf("non-exclusive provider tools = %v, want run_code beside the ordinary surface", names)
			}
		})
	}
}

// The exclusive surface is closed: a peer switch flipped after boot registers
// the peer tools but must not make them provider-visible.
func TestEffectPeerSwitchCannotWidenTheExclusiveSurface(t *testing.T) {
	home := statedBootHome(t)
	t.Setenv("REASONIX_HOME", home)
	writeFile(t, home, "config.toml", "[tools.ptc]\nenabled = true\nexclusive = true\n")
	dir := testenv.TempDir(t)
	rec := &browserScriptProvider{}
	kind := "boot-ptc-peer-" + strings.ToLower(t.Name())
	provider.Register(kind, func(provider.Config) (provider.Provider, error) { return rec, nil })
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "`+kind+`"
model = "x"
`)
	approveWorkspaceAt(t, home, dir)
	ctrl, err := Build(context.Background(), Options{Home: home, WorkspaceRoot: dir, Sink: &noticeRecorder{}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	yes := true
	if _, err := ctrl.SavePeersSettings(control.PeersPatch{Enabled: &yes, Dispatch: &yes}); err != nil {
		t.Fatalf("SavePeersSettings: %v", err)
	}
	if err := ctrl.Run(context.Background(), "hi again"); err != nil {
		t.Fatalf("Run after enabling peers: %v", err)
	}
	reqs := agentRequests(rec.requests())
	if len(reqs) < 2 {
		t.Fatalf("executor sampled %d times, want a turn after the switch", len(reqs))
	}
	names := toolSchemaNames(reqs[len(reqs)-1].Tools)
	if len(names) != 1 || names[0] != runcode.Name {
		t.Fatalf("provider tools after the peer switch = %v, want only %s", names, runcode.Name)
	}
}

// A tool the user disabled is still registered when attachPTC renders the
// contract (the registry drops it at the end of controller), so the contract
// must consult the disabled list itself instead of advertising a tool every
// program call would be refused.
func TestPTCContractOmitsDisabledTools(t *testing.T) {
	t.Parallel()
	home := statedBootHome(t)
	writeFile(t, home, "config.toml", "[tools.ptc]\nenabled = true\n")
	dir := testenv.TempDir(t)
	kind := "boot-ptc-contract-" + strings.ToLower(t.Name())
	provider.Register(kind, func(provider.Config) (provider.Provider, error) { return &browserScriptProvider{}, nil })
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[tools]
disabled = ["bash"]

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "`+kind+`"
model = "x"
`)
	approveWorkspaceAt(t, home, dir)
	res, err := BuildRuntime(context.Background(), Options{Home: home, WorkspaceRoot: dir, Sink: &noticeRecorder{}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer res.Controller.Close()
	if strings.Contains(res.Assembly.SystemPrompt, "tools.bash(") {
		t.Fatalf("system prompt advertises the disabled bash tool:\n%s", res.Assembly.SystemPrompt)
	}
	if !strings.Contains(res.Assembly.SystemPrompt, "tools.read_file(") {
		t.Fatalf("system prompt lost an enabled tool's contract:\n%s", res.Assembly.SystemPrompt)
	}
}

// ptcProxyTool is a use_capability-shaped CallResolver: its schema declares
// read-only, but the target it resolves to may be a writer.
type ptcProxyTool struct{}

func (ptcProxyTool) Name() string            { return "use_capability" }
func (ptcProxyTool) Description() string     { return "Call a deferred capability." }
func (ptcProxyTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (ptcProxyTool) ReadOnly() bool          { return true }
func (ptcProxyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
func (ptcProxyTool) ResolveCall(context.Context, json.RawMessage) (tool.ResolvedCall, error) {
	return tool.ResolvedCall{DisplayName: "use_capability", TargetName: "write_file", ReadOnly: false}, nil
}

// parallel may only run what it can prove reads, and a proxy's read-only schema
// proves nothing about the target: ptc must mark it so the engine refuses it.
func TestPTCToolsMarkProxyToolsParallelUnsafe(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	reg.Add(ptcReadTool{})
	reg.Add(ptcProxyTool{})
	got := ptcTools(reg, nil, nil)
	byName := make(map[string]ptc.Tool, len(got))
	for _, tl := range got {
		byName[tl.Name] = tl
	}
	for _, name := range []string{"read_file", "use_capability"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("ptcTools = %v, missing %s", got, name)
		}
	}
	if byName["read_file"].ParallelUnsafe {
		t.Fatalf("read_file = %+v, want ParallelUnsafe false", byName["read_file"])
	}
	if !byName["use_capability"].ParallelUnsafe {
		t.Fatalf("use_capability = %+v, want ParallelUnsafe true", byName["use_capability"])
	}
}

func runCodeCall(id, code string) provider.ToolCall {
	args, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		panic(err)
	}
	return provider.ToolCall{ID: id, Name: runcode.Name, Arguments: string(args)}
}

// A program's tools.parallel runs read-only tools, and use_capability declares
// itself read-only whatever target it later resolves to. Refusing it is the
// only safe answer: running it in parallel would write beside the read calls.
func TestEffectPTCParallelRefusesProxyWriter(t *testing.T) {
	run := buildPostureRun(t, "[tools.ptc]\nenabled = true\nexclusive = true\n", runCodeCall("p", `
const r = tools.parallel([
  {name: "use_capability", args: {action: "call", capability_id: "tool:write_file", arguments: {path: "parallel.txt", content: "written"}}}
]);
log("ok:", r[0].ok);
log("error:", r[0].error);
return "parallel done";
`))
	run.runIn(t, control.ToolApprovalYolo)
	if _, err := os.Stat(filepath.Join(run.dir, "parallel.txt")); err == nil {
		t.Fatal("use_capability wrote through tools.parallel")
	}
	saw := run.modelSaw(t, "p")
	for _, want := range []string{"ok: false", `parallel cannot run the proxy tool "use_capability"; call it directly`, "parallel done"} {
		if !strings.Contains(saw, want) {
			t.Fatalf("the program was told %q, missing %q", saw, want)
		}
	}
}

// Exclusive PTC leaves run_code as the model's only tool, so Plan mode has to
// admit it for research to happen at all. What the program does is still
// phase-checked call by call: the read lands, the write is refused.
func TestEffectPTCExclusivePlanAllowsReadsAndRefusesWrites(t *testing.T) {
	run := buildPostureRun(t, "[tools.ptc]\nenabled = true\nexclusive = true\n", runCodeCall("p", `
const src = tools.read_file({path: "src.txt"});
log("read:", src.trim());
const w = tools.tryCall("write_file", {path: "written.txt", content: "nope"});
log("write ok:", w.ok);
log("write error:", w.error);
return "plan program done";
`))
	writeFile(t, run.dir, "src.txt", "hello\n")
	run.ctrl.SetPlanMode(true)
	run.runIn(t, control.ToolApprovalYolo)
	if _, err := os.Stat(filepath.Join(run.dir, "written.txt")); err == nil {
		t.Fatal("a program wrote during Plan mode")
	}
	saw := run.modelSaw(t, "p")
	for _, want := range []string{"read: hello", "write ok: false", "side effect during planning", "plan program done"} {
		if !strings.Contains(saw, want) {
			t.Fatalf("the model read %q, missing %q", saw, want)
		}
	}
	if strings.Contains(saw, "Plan mode is still planning") {
		t.Fatalf("run_code itself was refused during Plan mode: %q", saw)
	}
}

// Outside exclusive mode run_code is an ordinary writer, and Plan mode keeps
// refusing it before any of its program runs.
func TestEffectPTCNonExclusivePlanStillRefusesRunCode(t *testing.T) {
	run := buildPostureRun(t, "[tools.ptc]\nenabled = true\nexclusive = false\n", runCodeCall("p", `
return tools.write_file({path: "written.txt", content: "nope"});
`))
	run.ctrl.SetPlanMode(true)
	run.runIn(t, control.ToolApprovalYolo)
	if _, err := os.Stat(filepath.Join(run.dir, "written.txt")); err == nil {
		t.Fatal("a program wrote during Plan mode")
	}
	saw := run.modelSaw(t, "p")
	for _, want := range []string{`"run_code"`, "Plan mode is still planning", "declared_writer"} {
		if !strings.Contains(saw, want) {
			t.Fatalf("the model read %q, missing %q", saw, want)
		}
	}
}

// The program writes, reads back, and is refused an rm. Written as the body of
// a synchronous function: no await, no TypeScript.
const ptcUnderTest = `
const made = tools.write_file({path: "made.txt", content: "from the program"});
log(tools.read_file({path: "made.txt"}));
const r = tools.tryCall("bash", {command: "rm -rf made.txt"});
log("rm ok:", r.ok);
return "program done";
`

// A run_code program's calls pass the same stages the model's own do: the write
// lands and is owed verification, the read sees it, the deny rule refuses rm,
// each call shows as its own card, and only what the program logged or returned
// enters the conversation. In exclusive mode run_code is also the only tool in
// the provider schema.
func TestEffectRunCodeCallsPassTheOrdinaryChecks(t *testing.T) {
	t.Parallel()
	home := statedBootHome(t)
	// PTC is the user's to enable: a cloned repo may not hand itself a program
	// that can call every tool.
	writeFile(t, home, "config.toml", holdChangedTurns+"\n[tools.ptc]\nenabled = true\nexclusive = true\n")
	dir := testenv.TempDir(t)
	rec := &browserScriptProvider{rounds: []func(string) *provider.ToolCall{
		func(string) *provider.ToolCall {
			return browserCall("s1", "run_code", map[string]any{"code": ptcUnderTest})
		},
	}}
	kind := "boot-ptc-" + strings.ToLower(t.Name())
	provider.Register(kind, func(provider.Config) (provider.Provider, error) { return rec, nil })
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"
tool_approval = "yolo"

[agent]
system_prompt = "BASE"

[permissions]
deny = ["Bash(rm:*)"]

[codegraph]
enabled = false

[[providers]]
name = "test-model"
kind = "`+kind+`"
model = "x"
`)
	approveWorkspaceAt(t, home, dir)
	sink := &noticeRecorder{}
	ctrl, err := Build(context.Background(), Options{Home: home, WorkspaceRoot: dir, Sink: sink})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var unready *agent.FinalReadinessError
	if err := ctrl.Run(context.Background(), "make a file with a program"); !errors.As(err, &unready) {
		t.Fatalf("Run = %v, want the readiness gate to ask for verification of the nested write", err)
	}
	ctrl.Close()
	reqs := agentRequests(rec.requests())

	// Exclusive PTC: run_code is the whole surface.
	if got := toolSchemaNames(reqs[0].Tools); len(got) != 1 || got[0] != "run_code" {
		t.Fatalf("provider tools = %v, want only run_code", got)
	}
	// The contract travels in the system prompt, so the model can write a
	// program against tools it never sees as schemas.
	if sys := reqs[0].Messages[0].Content; !strings.Contains(sys, "tools.read_file") || !strings.Contains(sys, "tools.bash") {
		t.Fatalf("system prompt lacks the run_code contract:\n%s", sys)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "made.txt")); err != nil || string(body) != "from the program" {
		t.Fatalf("made.txt = %q, %v", body, err)
	}
	results := effectToolResults(reqs[len(reqs)-1])
	if len(results) != 1 {
		t.Fatalf("tool results = %q, want only the program's", results)
	}
	for _, want := range []string{"from the program", "rm ok: false", "program done", "3 tool call(s)"} {
		if !strings.Contains(results[0], want) {
			t.Fatalf("program result = %q, want %q", results[0], want)
		}
	}
	cards := map[string]bool{}
	sink.mu.Lock()
	for _, e := range sink.events {
		if e.Kind == event.ToolResult {
			cards[e.Tool.ID+" "+e.Tool.Name] = true
		}
	}
	sink.mu.Unlock()
	for _, want := range []string{"s1.1 write_file", "s1.2 read_file", "s1.3 bash", "s1 run_code"} {
		if !cards[want] {
			t.Fatalf("no tool card %q among %v", want, cards)
		}
	}
}
