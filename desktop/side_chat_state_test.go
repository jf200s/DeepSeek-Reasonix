package main

import "testing"

// TestSaveTabsCollectSkipsSideChatTabs pins the process-local contract: a side
// chat tab never enters the persisted tab snapshot, so a restart cannot restore
// a companion session whose owner is gone.
func TestSaveTabsCollectSkipsSideChatTabs(t *testing.T) {
	app := testAppWithOrderedTabs(t, "parent", "parent", "side")
	app.tabs["side"].SideChat.Enabled = true
	app.tabs["side"].SideChat.ParentID = "parent"

	_, entries, activeID, _ := app.saveTabsCollectLocked()
	if len(entries) != 1 || entries[0].ID != "parent" {
		t.Fatalf("side chat tab must not be persisted; entries=%+v", entries)
	}
	if activeID != "parent" {
		t.Fatalf("the active persisted tab must be selected; got %q", activeID)
	}
}
