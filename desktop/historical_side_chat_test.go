package main

import (
	"encoding/json"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func sideChatSessionFixture(t *testing.T, root, id string) {
	t.Helper()
	service, err := session.NewService("side-chat-source", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(t.Context()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id, Kind: session.SessionKindSideChat})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "user", Role: provider.RoleUser, Content: "what does this function do"}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "message", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
}

// TestHistoricalCatalogOmitsSideChatSessions pins the product contract: a
// companion session is not a conversation the sidebar offers, exactly like a
// headless run.
func TestHistoricalCatalogOmitsSideChatSessions(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "ordinary-conversation")
	sideChatSessionFixture(t, config.SessionStoreDir(), "51deca70000000000000000000000001")
	app := newHistoricalLifecycleApp(t)
	installSessionCatalogForTest(t, app, config.SessionDir(), "global", "")

	management, err := app.ListHistoricalSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(management.Items) != 1 {
		t.Errorf("historical management lists %d sources, want only the conversation: %+v", len(management.Items), management.Items)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Label == "51deca70000000000000000000000001" {
		t.Errorf("sidebar rows = %+v, want only the ordinary conversation", page.Items)
	}
	if rows := app.listSessionsFromDir(config.SessionDir(), ""); len(rows) != 1 {
		t.Errorf("history rows = %+v, want only the ordinary conversation", rows)
	}
}
