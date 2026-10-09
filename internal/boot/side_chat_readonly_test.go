package boot

// Effect test for the side-chat (read-only) session surface. It asserts at the
// final provider boundary, like the other effect tests: what actually reaches
// the provider is what the model can use.

import (
	"context"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
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
