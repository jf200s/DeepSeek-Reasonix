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

// TestBuildSideChatCompanionNamesItsOwner pins the owner link: the companion
// names the conversation it belongs to in its prefix, and the owner's changing
// goal and activity stay out of the prefix and the session-context snapshot —
// they arrive per turn as pinned standing context instead.
func TestBuildSideChatCompanionNamesItsOwner(t *testing.T) {
	system, context := companionPrompt(t, "side-chat-companion", &SideChatParent{
		Title:     "体素村庄",
		SessionID: "desktop-owner-1",
	})

	if !strings.Contains(system, "体素村庄") || !strings.Contains(system, "desktop-owner-1") {
		t.Fatalf("the companion must name its owner conversation in the system prompt:\n%s", system)
	}
	if strings.Contains(context, "Companion session") {
		t.Fatalf("the owner's live facts must not ride the session-context snapshot:\n%s", context)
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

// TestSideChatCompanionHalvesSplitByStability pins the stability split: only the
// owner's identity reaches the prefix, so it stays byte-stable per owner, and no
// owner fact enters session context at all.
func TestSideChatCompanionHalvesSplitByStability(t *testing.T) {
	const base = "BASE"
	owner := &SideChatParent{Title: "owner", SessionID: "desktop-owner-2"}

	if got, want := appendSideChatCompanionPolicy(base, owner), appendSideChatCompanionPolicy(base, &SideChatParent{Title: "owner", SessionID: "desktop-owner-2"}); got != want {
		t.Fatalf("the same owner identity must render the same prefix:\nfirst:\n%s\nsecond:\n%s", got, want)
	}
	if appendSideChatCompanionPolicy(base, owner) == appendSideChatCompanionPolicy(base, &SideChatParent{Title: "other", SessionID: "desktop-owner-2"}) {
		t.Fatal("a different owner must change the prefix")
	}
	if got := appendSideChatCompanionPolicy(base, nil); got != base {
		t.Fatalf("a session without an owner keeps the plain prompt; got:\n%s", got)
	}
}
