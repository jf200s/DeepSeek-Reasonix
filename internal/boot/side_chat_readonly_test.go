package boot

// Effect test for the side-chat (read-only) session surface. It asserts at the
// final provider boundary, like the other effect tests: what actually reaches
// the provider is what the model can use.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// TestBuildReadOnlySessionStripsWriterTools pins the read-only session surface:
// a side-chat session must reach the provider without writer tools, and its
// shell must be the permission-classified read-only wrapper.
func TestBuildReadOnlySessionStripsWriterTools(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	registerBootTokenProfileTestProvider()
	prov := testutil.NewMock("side-chat-readonly", testutil.Turn{Text: "ok"})
	setBootTokenProfileTestProvider(t, prov)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"
[agent]
system_prompt = "BASE"
completion_validation = "off"

[[providers]]
name = "test-model"
kind = "boot-token-profile-test"
model = "x"
`)
	approveWorkspace(t, dir)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, ReadOnlySession: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "inspect the repository"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := prov.Requests()
	if len(reqs) == 0 {
		t.Fatal("read-only session issued no provider request")
	}
	req := reqs[0]

	for _, banned := range []string{"write_file", "edit_file", "todo_write", "set_session_title", "task", "complete_subtask"} {
		if requestHasTool(req, banned) {
			t.Fatalf("read-only session must not expose %s; tools=%v", banned, toolSchemaNames(req.Tools))
		}
	}
	if !requestHasTool(req, "read_file") {
		t.Fatalf("read-only session should keep read_file; tools=%v", toolSchemaNames(req.Tools))
	}
	shellName := platformShellToolName()
	if !requestHasTool(req, shellName) {
		t.Fatalf("read-only session should keep %s behind the read-only wrapper; tools=%v", shellName, toolSchemaNames(req.Tools))
	}
	if !requestToolDescriptionContains(req, shellName, "Only permission-classified read-only commands are allowed") {
		t.Fatalf("read-only session %s must be the permission-layer wrapper; got %q", shellName, requestToolDescription(req, shellName))
	}
}

// TestBuildWritableSessionKeepsWriterTools is the control: the same provider
// configuration without the flag must still expose writer tools, so the flag
// itself is what strips them.
func TestBuildWritableSessionKeepsWriterTools(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	registerBootTokenProfileTestProvider()
	prov := testutil.NewMock("side-chat-writable", testutil.Turn{Text: "ok"})
	setBootTokenProfileTestProvider(t, prov)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"
[agent]
system_prompt = "BASE"
completion_validation = "off"

[[providers]]
name = "test-model"
kind = "boot-token-profile-test"
model = "x"
`)
	approveWorkspace(t, dir)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "inspect the repository"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := prov.Requests()
	if len(reqs) == 0 {
		t.Fatal("writable session issued no provider request")
	}
	req := reqs[0]
	if !requestHasTool(req, "write_file") {
		t.Fatalf("a writable session must keep write_file; tools=%v", toolSchemaNames(req.Tools))
	}
	shellName := platformShellToolName()
	if requestToolDescriptionContains(req, shellName, "Only permission-classified read-only commands are allowed") {
		t.Fatalf("a writable session must not use the read-only %s wrapper; got %q", shellName, requestToolDescription(req, shellName))
	}
}

// readOnlySessionProbeShell stands in for the built-in shell: it accepts every
// command, so a refusal can only have come from the read-only wrapper.
type readOnlySessionProbeShell struct{ name string }

func (s readOnlySessionProbeShell) Name() string { return s.name }

func (readOnlySessionProbeShell) Description() string {
	return "Execute a command in the shell and return combined stdout/stderr."
}

func (readOnlySessionProbeShell) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}

func (readOnlySessionProbeShell) ReadOnly() bool { return false }

func (readOnlySessionProbeShell) Execute(context.Context, json.RawMessage) (string, error) {
	return "ran", nil
}

// readOnlySessionProbeWriter is the writer the probe expects the read-only
// registry to drop.
type readOnlySessionProbeWriter struct{}

func (readOnlySessionProbeWriter) Name() string        { return "write_file" }
func (readOnlySessionProbeWriter) Description() string { return "Write a file." }
func (readOnlySessionProbeWriter) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (readOnlySessionProbeWriter) ReadOnly() bool { return false }
func (readOnlySessionProbeWriter) Execute(context.Context, json.RawMessage) (string, error) {
	return "wrote", nil
}

// TestReadOnlySessionRefusesWriteCommandsAtExecutionTime pins the other half of
// the read-only surface. TestBuildReadOnlySessionStripsWriterTools shows what the
// provider may call, but a hidden tool is not the same guarantee as a refused
// call: this drives the shell the read-only session installs with a write
// command and requires the host to refuse it.
func TestReadOnlySessionRefusesWriteCommandsAtExecutionTime(t *testing.T) {
	reg := tool.NewRegistry()
	shellName := platformShellToolName()
	reg.Add(readOnlySessionProbeShell{name: shellName})
	reg.Add(readOnlySessionProbeWriter{})

	shell, ok := readOnlySessionRegistry(reg, true).Get(shellName)
	if !ok {
		t.Fatalf("read-only session registry must keep the shell; got %v", readOnlySessionRegistry(reg, true).Names())
	}
	if !shell.ReadOnly() {
		t.Fatal("the read-only session shell must report ReadOnly")
	}
	if _, ok := readOnlySessionRegistry(reg, true).Get("write_file"); ok {
		t.Fatal("read-only session registry must drop write_file")
	}

	if out, err := shell.Execute(context.Background(), json.RawMessage(`{"command":"git status"}`)); err != nil || out != "ran" {
		t.Fatalf("a read-only command must reach the shell: out=%q err=%v", out, err)
	}

	for _, command := range []string{
		`Set-Content -Path probe.txt -Value x`,
		`echo x > probe.txt`,
		`rm -rf probe`,
	} {
		args, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		out, execErr := shell.Execute(context.Background(), args)
		if msg, blocked := tool.BlockedMessage(execErr); !blocked || !strings.HasPrefix(msg, "blocked:") {
			t.Fatalf("write command %q must be refused at execution time; out=%q err=%v", command, out, execErr)
		} else if out != "" {
			t.Fatalf("refused command %q must not also return output; got %q", command, out)
		}
	}

	// Control: without the flag the same registry leaves the shell unwrapped, so
	// the refusals above come from the read-only boundary and not from the probe.
	plain, ok := readOnlySessionRegistry(reg, false).Get(shellName)
	if !ok {
		t.Fatalf("writable registry must keep the shell; got %v", readOnlySessionRegistry(reg, false).Names())
	}
	args, err := json.Marshal(map[string]string{"command": `Set-Content -Path probe.txt -Value x`})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := plain.Execute(context.Background(), args); err != nil || out != "ran" {
		t.Fatalf("a writable session must not wrap the shell: out=%q err=%v", out, err)
	}
}
