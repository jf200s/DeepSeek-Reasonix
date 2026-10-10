package main

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

const (
	// sideChatCloseGrace bounds how long a companion close waits for a cancelled
	// turn to drain before falling back to the detach close.
	sideChatCloseGrace = 1500 * time.Millisecond
	sideChatClosePoll  = 25 * time.Millisecond
	// sideChatOwnerStandingContextPath names the synthetic standing-context entry
	// that carries the owner's live goal and recent activity. It is not a file on
	// disk and never enters the tab's pinned-file list.
	sideChatOwnerStandingContextPath = "companion/owner-context"
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
	tab.SideChat.ParentTitle = parent.TopicTitle
	tab.SideChat.ParentSessionID = parent.SessionID

	a.mu.Lock()
	if a.tabs[parentTabID] != parent {
		a.mu.Unlock()
		return SideChatOpenResult{}, fmt.Errorf("side chat: tab %q closed while opening", parentTabID)
	}
	tab.SideChat.Ordinal = nextSideChatOrdinalLocked(a.tabs, parentTabID)
	// A companion needs its own sink: the generic tab factory leaves it nil, and
	// without one its whole turn is emitted nowhere. The owner keeps focus, so
	// this deliberately does not activate the tab.
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
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
	a.cancelSideChatTurn(tab.Ctrl)
	if err := a.closeTab(tabID, false); err != nil {
		// The cancelled turn had not finished draining. Fall back to the detach
		// close: the tab still goes away, and the session delete below stops the
		// runtime the detach handed off.
		if detachErr := a.closeTab(tabID, true); detachErr != nil {
			return err
		}
	}
	if service == nil {
		// No exclusive session to delete; warn so a silent leak stays visible.
		slog.Warn("desktop: side chat closed with no session to delete", "tab", tabID)
		return nil
	}
	return service.Delete(a.bootContext(), ref)
}

// cancelSideChatTurn stops a running companion turn so the close can proceed.
// A companion is throwaway, so closing it cancels instead of being refused the
// way an owner's tab would be: the design promises the user that closing a
// companion cancels its turn and then deletes its throwaway session.
func (a *App) cancelSideChatTurn(ctrl control.SessionAPI) {
	if ctrl == nil || !controllerHasActiveRuntimeWork(ctrl) {
		return
	}
	ctrl.Cancel()
	ctx := a.bootContext()
	deadline := time.Now().Add(sideChatCloseGrace)
	for time.Now().Before(deadline) {
		if !controllerHasActiveRuntimeWork(ctrl) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sideChatClosePoll):
		}
	}
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

// ensureFirstSessionPath creates a tab's first session store. A companion
// session records its kind, so conversation lists leave it out.
func (a *App) ensureFirstSessionPath(tabID string, ctrl control.SessionAPI) {
	if a.tabIsSideChat(tabID) {
		if ensurer, ok := ctrl.(interface{ EnsureSideChatSessionPath() }); ok {
			ensurer.EnsureSideChatSessionPath()
		}
		return
	}
	if ensurer, ok := ctrl.(interface{ EnsureSessionPath() }); ok {
		ensurer.EnsureSessionPath()
	}
}

func (a *App) tabIsSideChat(tabID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabs[tabID]
	return tab != nil && tab.SideChat.Enabled
}

// sessionKindForTab names what a tab's first session is, so conversation lists
// can leave it out. A desktop tab binds its session while its controller is
// built, which is before its first input ever arrives: recording the kind at
// creation is the only point that works, and EnsureSideChatSessionPath is a
// no-op by the time a companion submits.
func sessionKindForTab(tab *WorkspaceTab) session.SessionKind {
	if tab != nil && tab.SideChat.Enabled {
		return session.SessionKindSideChat
	}
	return ""
}

// sideChatParentContext summarises the owner's recent activity so a companion
// can tell which conversation it belongs to without inheriting its transcript.
// Only the last few exchanges are kept, each truncated: enough to name the work
// in progress, never a copy of the owner's context.
func sideChatParentContext(parent *WorkspaceTab) string {
	if parent == nil || parent.Ctrl == nil {
		return ""
	}
	history := parent.Ctrl.History()
	if len(history) == 0 {
		return ""
	}
	const keep, limit = 6, 240
	tail := history
	if len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	var b strings.Builder
	for _, message := range tail {
		text := strings.TrimSpace(message.Content)
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > limit {
			text = string([]rune(text)[:limit]) + "…"
		}
		role := strings.TrimSpace(string(message.Role))
		if role == "" {
			role = "message"
		}
		b.WriteString("- " + role + ": " + text + "\n")
	}
	return strings.TrimSpace(b.String())
}

// sideChatOwnerStandingContext returns the owner's live goal and recent
// activity for a companion tab. Reading it per turn lets the companion follow
// its owner through standing context, which appends a revision only when this
// text actually changes.
func (a *App) sideChatOwnerStandingContext(tabID string) string {
	tab := a.tabByID(tabID)
	if tab == nil {
		return ""
	}
	a.mu.RLock()
	owned := tab.SideChat.Enabled
	parentID := tab.SideChat.ParentID
	a.mu.RUnlock()
	if !owned || parentID == "" {
		return ""
	}
	parent := a.tabByID(parentID)
	if parent == nil {
		return ""
	}
	goal := strings.TrimSpace(currentTabGoal(parent))
	activity := strings.TrimSpace(sideChatParentContext(parent))
	if goal == "" && activity == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("The conversation this companion belongs to.\n")
	if goal != "" {
		b.WriteString("goal: " + goal + "\n")
	}
	if activity != "" {
		b.WriteString("recent activity:\n" + activity)
	}
	return b.String()
}

// sideChatParentForBoot hands the stable owner facts to the companion's boot
// options. The owner's goal and recent activity deliberately stay out of them:
// they arrive per turn as standing context instead.
func sideChatParentForBoot(tab *WorkspaceTab) *boot.SideChatParent {
	if tab == nil || !tab.SideChat.Enabled {
		return nil
	}
	return &boot.SideChatParent{
		Title:     tab.SideChat.ParentTitle,
		SessionID: tab.SideChat.ParentSessionID,
	}
}
