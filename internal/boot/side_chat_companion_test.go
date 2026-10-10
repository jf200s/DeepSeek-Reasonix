package boot

// A companion must be able to name the conversation it belongs to. The owner's
// identity is folded into this session's cache-stable system prompt, while the
// owner's goal and activity ride the host-authored session-context snapshot.

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
)

// companionPrompt builds a read-only companion for parent and returns what the
// provider saw: the system prompt and the session-context block.
func companionPrompt(t *testing.T, name string, parent *SideChatParent) (string, string) {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	registerBootTokenProfileTestProvider()
	prov := testutil.NewMock(name, testutil.Turn{Text: "ok"})
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

	ctrl, err := Build(context.Background(), Options{
		Sink:            event.Discard,
		ReadOnlySession: true,
		SideChatParent:  parent,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "这个函数做什么"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := prov.Requests()
	if len(reqs) == 0 {
		t.Fatal("companion session issued no provider request")
	}
	return systemMessage(reqs[0].Messages), sessionContextMessage(reqs[0].Messages)
}

// TestBuildSideChatCompanionNamesItsOwner pins both halves of the owner link:
// the identity in the prefix, the goal and activity in session context.
func TestBuildSideChatCompanionNamesItsOwner(t *testing.T) {
	system, context := companionPrompt(t, "side-chat-companion", &SideChatParent{
		Title:     "体素村庄",
		SessionID: "desktop-owner-1",
		Goal:      "修好构建",
		Context:   "- user: 继续\n- assistant: 收到",
	})

	if !strings.Contains(system, "体素村庄") || !strings.Contains(system, "desktop-owner-1") {
		t.Fatalf("the companion must name its owner conversation in the system prompt:\n%s", system)
	}
	if !strings.Contains(context, "修好构建") || !strings.Contains(context, "- user: 继续") {
		t.Fatalf("the companion's session context must carry the owner's goal and activity:\n%s", context)
	}
	if strings.Contains(system, "修好构建") {
		t.Fatalf("the owner's changing goal must stay out of the cache-stable prefix:\n%s", system)
	}
}

// TestBuildWithoutSideChatParentKeepsThePlainPrompt is the control: the same
// configuration without an owner adds neither half, so the link is what the
// option introduces and not something the prompt already had.
func TestBuildWithoutSideChatParentKeepsThePlainPrompt(t *testing.T) {
	system, context := companionPrompt(t, "side-chat-plain", nil)

	if strings.Contains(system, "# Side conversation") || strings.Contains(context, "Companion session") {
		t.Fatalf("a session without an owner must not claim one:\nsystem:\n%s\ncontext:\n%s", system, context)
	}
}

// TestSideChatCompanionHalvesSplitByStability pins the split directly: only the
// owner's identity reaches the prefix, so a changed goal cannot rewrite it,
// while the session-context half does change with the owner.
func TestSideChatCompanionHalvesSplitByStability(t *testing.T) {
	const base = "BASE"
	first := &SideChatParent{Title: "owner", SessionID: "desktop-owner-2", Goal: "第一个目标", Context: "- user: 甲"}
	second := &SideChatParent{Title: "owner", SessionID: "desktop-owner-2", Goal: "换了一个目标", Context: "- user: 乙"}

	if got, want := appendSideChatCompanionPolicy(base, first), appendSideChatCompanionPolicy(base, second); got != want {
		t.Fatalf("the owner's goal must not reach the prefix:\nfirst:\n%s\nsecond:\n%s", got, want)
	}
	if strings.Contains(appendSideChatCompanionPolicy(base, first), "第一个目标") {
		t.Fatal("the prefix must carry identity only")
	}
	if got := sideChatCompanionContextBlock(first); !strings.Contains(got, "第一个目标") || !strings.Contains(got, "- user: 甲") {
		t.Fatalf("session context must carry the owner's goal and activity:\n%s", got)
	}
	if sideChatCompanionContextBlock(first) == sideChatCompanionContextBlock(second) {
		t.Fatal("session context must follow the owner")
	}
	if got := appendSideChatCompanionPolicy(base, nil); got != base {
		t.Fatalf("a session without an owner keeps the plain prompt; got:\n%s", got)
	}
}
