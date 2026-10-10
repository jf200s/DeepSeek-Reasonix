package main

// The companion's owner link has three pieces the desktop side owns: the session
// kind recorded at creation, the facts captured when the companion opens, and
// the bounded summary of what the owner is doing.

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// TestSessionKindForTabNamesCompanions pins where the kind comes from: only a
// companion tab marks its first session, so conversation lists can leave it out
// while every other tab stays an ordinary conversation.
func TestSessionKindForTabNamesCompanions(t *testing.T) {
	if got := sessionKindForTab(nil); got != "" {
		t.Fatalf("a missing tab has no kind; got %q", got)
	}
	if got := sessionKindForTab(&WorkspaceTab{ID: "plain"}); got != "" {
		t.Fatalf("an ordinary tab stays an ordinary conversation; got %q", got)
	}
	companion := &WorkspaceTab{ID: "companion"}
	companion.SideChat.Enabled = true
	if got := sessionKindForTab(companion); got != session.SessionKindSideChat {
		t.Fatalf("a companion records the side-chat kind; got %q", got)
	}
}

// TestSideChatParentForBootCarriesOwnerFacts pins the hand-off: boot options see
// the stable facts captured at open time, and an ordinary tab offers none. The
// owner's live goal and activity deliberately stay out of boot options.
func TestSideChatParentForBootCarriesOwnerFacts(t *testing.T) {
	if sideChatParentForBoot(nil) != nil {
		t.Fatal("a missing tab has no owner facts")
	}
	if sideChatParentForBoot(&WorkspaceTab{ID: "plain"}) != nil {
		t.Fatal("an ordinary tab has no owner facts")
	}
	companion := &WorkspaceTab{ID: "companion"}
	companion.SideChat.Enabled = true
	companion.SideChat.ParentTitle = "体素村庄"
	companion.SideChat.ParentSessionID = "desktop-owner-1"

	got := sideChatParentForBoot(companion)
	if got == nil {
		t.Fatal("a companion must offer its owner facts")
	}
	if got.Title != "体素村庄" || got.SessionID != "desktop-owner-1" {
		t.Fatalf("owner facts reached boot options incomplete: %+v", got)
	}
}

// TestSideChatParentContextKeepsOnlyTheOwnersTail pins the summary: the most
// recent exchanges, each truncated, never the owner's whole transcript.
func TestSideChatParentContextKeepsOnlyTheOwnersTail(t *testing.T) {
	if got := sideChatParentContext(nil); got != "" {
		t.Fatalf("a missing owner has no summary; got %q", got)
	}
	if got := sideChatParentContext(&WorkspaceTab{ID: "unbound"}); got != "" {
		t.Fatalf("an owner without a controller has no summary; got %q", got)
	}

	ctrl := newTabScopedActionController()
	ctrl.history = nil
	for index := 0; index < 10; index++ {
		ctrl.history = append(ctrl.history, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("第%d条", index)})
	}
	ctrl.history = append(ctrl.history, provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("字", 400)})

	got := sideChatParentContext(&WorkspaceTab{ID: "parent", Ctrl: ctrl})
	if strings.Contains(got, "第0条") {
		t.Fatalf("only the owner's tail belongs in the summary:\n%s", got)
	}
	if !strings.Contains(got, "第9条") || !strings.Contains(got, "assistant:") {
		t.Fatalf("the summary must keep the most recent exchanges:\n%s", got)
	}
	if utf8.RuneCountInString(got) > 7*280 {
		t.Fatalf("the summary must stay bounded; got %d runes", utf8.RuneCountInString(got))
	}
}

// TestSideChatOwnerStandingContextFollowsTheOwner pins the live half of the owner
// link: a companion reports its owner's current goal and tail, so the text it
// feeds the standing-context channel changes exactly when the owner moves on.
func TestSideChatOwnerStandingContextFollowsTheOwner(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	parent := &WorkspaceTab{ID: "parent"}
	child := &WorkspaceTab{ID: "child"}
	child.SideChat.Enabled = true
	child.SideChat.ParentID = "parent"
	app.tabs["parent"], app.tabs["child"] = parent, child

	if got := app.sideChatOwnerStandingContext("child"); got != "" {
		t.Fatalf("an owner without a controller reports nothing; got %q", got)
	}
	if got := app.sideChatOwnerStandingContext("parent"); got != "" {
		t.Fatalf("an ordinary tab reports nothing; got %q", got)
	}
	if got := app.sideChatOwnerStandingContext("missing"); got != "" {
		t.Fatalf("an unknown tab reports nothing; got %q", got)
	}

	ctrl := newTabScopedActionController()
	ctrl.goal = "修好构建"
	ctrl.history = []provider.Message{{Role: provider.RoleUser, Content: "先看构建"}}
	parent.Ctrl = ctrl

	first := app.sideChatOwnerStandingContext("child")
	if !strings.Contains(first, "修好构建") || !strings.Contains(first, "先看构建") {
		t.Fatalf("the companion must report the owner's goal and activity:\n%s", first)
	}

	ctrl.goal = "改成补测试"
	ctrl.history = append(ctrl.history, provider.Message{Role: provider.RoleUser, Content: "改主意了"})
	second := app.sideChatOwnerStandingContext("child")
	if second == first {
		t.Fatal("unchanged text would append no revision, so the report must track the owner")
	}
	if !strings.Contains(second, "改成补测试") || !strings.Contains(second, "改主意了") {
		t.Fatalf("the report must carry the owner's latest state:\n%s", second)
	}
}
