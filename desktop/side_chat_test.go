package main

import "testing"

func sideChatTab(parent string, ordinal int) *WorkspaceTab {
	tab := &WorkspaceTab{}
	tab.SideChat.Enabled = true
	tab.SideChat.ParentID = parent
	tab.SideChat.Ordinal = ordinal
	return tab
}

// TestNextSideChatOrdinalReusesFreedNumbers pins the numbering contract: the
// smallest free number wins, so closing the first companion lets the next one
// take its number instead of growing forever.
func TestNextSideChatOrdinalReusesFreedNumbers(t *testing.T) {
	tabs := map[string]*WorkspaceTab{
		"parent": {},
		"other":  {},
		"a":      sideChatTab("parent", 1),
		"b":      sideChatTab("parent", 3),
		"c":      sideChatTab("other", 1),
	}
	if got := nextSideChatOrdinalLocked(tabs, "parent"); got != 2 {
		t.Fatalf("ordinal = %d, want 2: the smallest free number is reused", got)
	}
	if got := nextSideChatOrdinalLocked(tabs, "other"); got != 2 {
		t.Fatalf("ordinal for another owner = %d, want 2", got)
	}
	if got := nextSideChatOrdinalLocked(map[string]*WorkspaceTab{}, "parent"); got != 1 {
		t.Fatalf("first ordinal = %d, want 1", got)
	}
}

// TestSideChatChildIDsListOnlyThatOwnersCompanions keeps cascade cleanup honest:
// closing one owner must never close another owner's companion or a plain tab.
func TestSideChatChildIDsListOnlyThatOwnersCompanions(t *testing.T) {
	tabs := map[string]*WorkspaceTab{
		"parent": {},
		"other":  {},
		"a":      sideChatTab("parent", 1),
		"b":      sideChatTab("other", 1),
		"plain":  {},
	}
	got := sideChatChildIDsLocked(tabs, "parent")
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("children = %v, want [a]", got)
	}
	if got := sideChatChildIDsLocked(tabs, "plain"); len(got) != 0 {
		t.Fatalf("a tab without companions reported %v", got)
	}
}

// TestCompanionBootFieldsStayPaired pins the pairing every boot path must read
// from: a companion is read-only and carries its owner facts, an ordinary tab
// has neither. A companion booted writable is the regression this guards.
func TestCompanionBootFieldsStayPaired(t *testing.T) {
	if readOnly, parent := sideChatBootFields(nil); readOnly || parent != nil {
		t.Fatalf("a missing tab has neither; got readOnly=%v parent=%+v", readOnly, parent)
	}
	if readOnly, parent := sideChatBootFields(&WorkspaceTab{ID: "plain"}); readOnly || parent != nil {
		t.Fatalf("an ordinary tab is writable and ownerless; got readOnly=%v parent=%+v", readOnly, parent)
	}
	companion := &WorkspaceTab{ID: "companion"}
	companion.SideChat.Enabled = true
	companion.SideChat.ParentTitle = "体素村庄"
	companion.SideChat.ParentSessionID = "desktop-owner-1"

	readOnly, parent := sideChatBootFields(companion)
	if !readOnly {
		t.Fatal("a companion must boot read-only: otherwise it asks to widen its write scope")
	}
	if parent == nil || parent.Title != "体素村庄" || parent.SessionID != "desktop-owner-1" {
		t.Fatalf("a companion must carry its owner facts; got %+v", parent)
	}
}
