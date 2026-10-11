package session

import (
	"context"
	"testing"
)

// TestCreateAcceptsSideChatKind pins the store contract: a companion session
// records its kind, which is what lets a conversation list leave it out.
func TestCreateAcceptsSideChatKind(t *testing.T) {
	ctx := context.Background()
	persistence := NewFilesystemPersistence(t.TempDir())
	service, err := NewService("side-chat-kind", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(ctx) })

	const id = "51deca70000000000000000000000001"
	runtime, err := service.Create(ctx, CreateOptions{SessionID: id, Kind: SessionKindSideChat})
	if err != nil {
		t.Fatalf("Create with the side-chat kind: %v", err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })

	info, err := persistence.Stat(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != SessionKindSideChat {
		t.Fatalf("stored kind = %q, want %q", info.Kind, SessionKindSideChat)
	}
}

// TestCreateStillRejectsUnknownKinds keeps the supported set explicit.
func TestCreateStillRejectsUnknownKinds(t *testing.T) {
	ctx := context.Background()
	persistence := NewFilesystemPersistence(t.TempDir())
	service, err := NewService("side-chat-kind-reject", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(ctx) })

	if _, err := service.Create(ctx, CreateOptions{SessionID: "51deca70000000000000000000000002", Kind: SessionKind("not-a-kind")}); err == nil {
		t.Fatal("an unknown session kind must be rejected")
	}
}
