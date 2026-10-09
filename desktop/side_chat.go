package main

import (
	"fmt"
	"log/slog"
	"sort"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

// SideChatOpenResult identifies the companion session opened for a parent tab.
type SideChatOpenResult struct {
	TabID     string
	SessionID string
	Ordinal   int
}

// OpenSideChatForTab opens a read-only companion session owned by parentTabID.
// The owner keeps running and keeps focus; the companion lives in its own
// session directory, so the session catalog never lists it, and it is never
// persisted as a tab.
func (a *App) OpenSideChatForTab(parentTabID string) (SideChatOpenResult, error) {
	parent := a.tabByID(parentTabID)
	if parent == nil {
		return SideChatOpenResult{}, fmt.Errorf("side chat: tab %q is not open", parentTabID)
	}
	if a.tabIsReadOnly(parent) {
		return SideChatOpenResult{}, readOnlyChannelErr()
	}
	a.mu.RLock()
	scope, workspaceRoot := parent.Scope, parent.WorkspaceRoot
	a.mu.RUnlock()

	releaseAdmission, err := a.beginProjectRuntimeAdmission(scope, desktopWorkspaceRoot(scope, workspaceRoot))
	if err != nil {
		return SideChatOpenResult{}, err
	}
	defer releaseAdmission()

	tab := a.createTabEntryWithID(scope, workspaceRoot, newTopicID(), newTabID())
	tab.SideChat.Enabled = true
	tab.SideChat.ParentID = parentTabID

	a.mu.Lock()
	if a.tabs[parentTabID] != parent {
		a.mu.Unlock()
		return SideChatOpenResult{}, fmt.Errorf("side chat: tab %q closed while opening", parentTabID)
	}
	tab.SideChat.Ordinal = nextSideChatOrdinalLocked(a.tabs, parentTabID)
	a.tabs[tab.ID] = tab
	a.tabOrder = append(a.tabOrder, tab.ID)
	a.mu.Unlock()

	a.buildTabController(tab)
	a.mu.RLock()
	ready, started, sessionID, ordinal := tab.Ctrl != nil, tab.StartupErr, tab.SessionID, tab.SideChat.Ordinal
	a.mu.RUnlock()
	if !ready {
		if err := a.closeTab(tab.ID, false); err != nil {
			return SideChatOpenResult{}, fmt.Errorf("side chat: build runtime: %s (rollback: %w)", started, err)
		}
		return SideChatOpenResult{}, fmt.Errorf("side chat: build runtime: %s", started)
	}
	return SideChatOpenResult{TabID: tab.ID, SessionID: sessionID, Ordinal: ordinal}, nil
}

// CloseSideChatTab closes one companion tab and deletes its throwaway session.
// The owner tab and its own session are untouched.
func (a *App) CloseSideChatTab(tabID string) error {
	tab := a.tabByID(tabID)
	if tab == nil {
		return nil
	}
	a.mu.RLock()
	enabled := tab.SideChat.Enabled
	ref, service := sideChatSessionTarget(tab)
	a.mu.RUnlock()
	if !enabled {
		return fmt.Errorf("side chat: tab %q is not a companion session", tabID)
	}
	if err := a.closeTab(tabID, false); err != nil {
		return err
	}
	if service == nil {
		return nil
	}
	return service.Delete(a.bootContext(), ref)
}

// closeSideChatChildren closes every companion owned by parentTabID first, so
// closing an owner can never leave an orphaned companion behind.
func (a *App) closeSideChatChildren(parentTabID string) {
	for _, childID := range a.sideChatChildIDs(parentTabID) {
		if err := a.CloseSideChatTab(childID); err != nil {
			slog.Warn("desktop: side chat close before parent failed", "parent", parentTabID, "child", childID, "err", err)
		}
	}
}

func (a *App) sideChatChildIDs(parentTabID string) []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return sideChatChildIDsLocked(a.tabs, parentTabID)
}

func sideChatChildIDsLocked(tabs map[string]*WorkspaceTab, parentTabID string) []string {
	ids := make([]string, 0, 1)
	for id, tab := range tabs {
		if tab != nil && tab.SideChat.Enabled && tab.SideChat.ParentID == parentTabID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// nextSideChatOrdinalLocked returns the smallest ordinal free among the parent's
// current companions, so closing one releases its number for the next one.
func nextSideChatOrdinalLocked(tabs map[string]*WorkspaceTab, parentTabID string) int {
	used := make(map[int]bool)
	for _, id := range sideChatChildIDsLocked(tabs, parentTabID) {
		used[tabs[id].SideChat.Ordinal] = true
	}
	for ordinal := 1; ; ordinal++ {
		if !used[ordinal] {
			return ordinal
		}
	}
}

// sideChatSessionTarget reads the companion's own session identity before a
// close detaches its controller; a shared or unbound session is not deleted.
func sideChatSessionTarget(tab *WorkspaceTab) (session.SessionRef, *session.Service) {
	identity, ok := tab.Ctrl.(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return session.SessionRef{}, nil
	}
	ref, bound := identity.SessionRef()
	if !bound {
		return session.SessionRef{}, nil
	}
	return ref, identity.SessionService()
}
