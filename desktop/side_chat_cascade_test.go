package main

import "testing"

// TestCloseTabCascadesToSideChatChildren pins the cascade contract: closing an
// owner closes its companion sessions first, so no companion can outlive the tab
// that owns it, and unrelated tabs are left alone. A companion that survived its
// owner would keep a live read-only session nobody can reach or close.
func TestCloseTabCascadesToSideChatChildren(t *testing.T) {
	app := testAppWithOrderedTabs(t, "parent", "parent", "other", "child")
	app.tabs["child"].SideChat.Enabled = true
	app.tabs["child"].SideChat.ParentID = "parent"

	// A snapshot of an in-memory tab may fail, but the cascade runs before that
	// path, so the companion outcome is what this test pins.
	_ = app.CloseTab("parent")

	app.mu.RLock()
	_, childOpen := app.tabs["child"]
	_, otherOpen := app.tabs["other"]
	app.mu.RUnlock()
	if childOpen {
		t.Fatal("closing the owner must close its companion")
	}
	if !otherOpen {
		t.Fatal("closing one owner must not close an unrelated tab")
	}
}

// TestCloseSideChatTabRejectsANonCompanion pins the guard: the close command is
// only meaningful for a tab the host opened as a companion, so an ordinary tab is
// reported instead of being closed by a stale dock action.
func TestCloseSideChatTabRejectsANonCompanion(t *testing.T) {
	app := testAppWithOrderedTabs(t, "plain", "plain")
	if err := app.CloseSideChatTab("plain"); err == nil {
		t.Fatal("closing a non-companion tab must report an error")
	}
	app.mu.RLock()
	_, stillOpen := app.tabs["plain"]
	app.mu.RUnlock()
	if !stillOpen {
		t.Fatal("a rejected close must leave the tab in place")
	}
}
