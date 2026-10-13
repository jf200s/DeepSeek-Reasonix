package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// TestSidebarOmitsCanonicalCompanionSessions pins the sidebar contract on the
// canonical path. A companion's session is minted through bindFreshDesktopSession
// like any other desktop tab's first session, so it lands in the workspace
// registry as an ordinary member; the sidebar must still leave it out, exactly
// like a headless run. TestHistoricalCatalogOmitsSideChatSessions only covers
// the legacy migration source, which is why this path was able to leak.
func TestSidebarOmitsCanonicalCompanionSessions(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	service := app.desktopSessionService("")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	creator := registrySessionCreator{service: service}

	ordinary, workspaceID, err := app.bindFreshDesktopSession(t.Context(), "project", project, creator, "")
	if err != nil {
		t.Fatalf("bind ordinary session: %v", err)
	}
	companion, _, err := app.bindFreshDesktopSession(t.Context(), "project", project, creator, session.SessionKindSideChat)
	if err != nil {
		t.Fatalf("bind companion session: %v", err)
	}

	// The companion really is a registry member — otherwise the assertion below
	// would pass for the wrong reason.
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatalf("Load workspace state: %v", err)
	}
	members := map[string]bool{}
	for _, id := range state.Workspaces[workspaceID].SessionIDs {
		members[id] = true
	}
	if !members[ordinary.SessionID] || !members[companion.SessionID] {
		t.Fatalf("workspace %s members = %#v, want both %s and %s",
			workspaceID, state.Workspaces[workspaceID].SessionIDs, ordinary.SessionID, companion.SessionID)
	}

	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: project, Limit: 50})
	if err != nil {
		t.Fatalf("ListProjectTopics: %v", err)
	}
	seen := map[string]bool{}
	for _, node := range page.Items {
		if node.Session != nil {
			seen[node.Session.SessionID] = true
		}
	}
	if seen[companion.SessionID] {
		t.Errorf("sidebar offers the companion session %s, want it omitted like a headless run", companion.SessionID)
	}
	if !seen[ordinary.SessionID] {
		t.Errorf("sidebar rows = %+v, want the ordinary conversation %s", page.Items, ordinary.SessionID)
	}
}
