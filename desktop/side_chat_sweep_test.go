package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/session"
)

// TestSweepSideChatOrphansKeepsLiveAndOrdinarySessions pins the sweep contract
// against a real session store: an unheld companion is debris and goes, while
// an ordinary conversation and a companion whose writer is held are kept. The
// live case is what keeps the sweep safe beside a second dev instance.
func TestSweepSideChatOrphansKeepsLiveAndOrdinarySessions(t *testing.T) {
	root := t.TempDir()
	service, err := session.NewService("side-chat-sweep", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })

	create := func(id string, kind session.SessionKind) {
		t.Helper()
		runtime, err := service.Create(context.Background(), session.CreateOptions{SessionID: id, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		// Create returns a bound runtime; release the writer lease so the store
		// looks like one a dead process left behind.
		if runtime != nil {
			if err := service.Close(context.Background(), runtime.Ref()); err != nil {
				t.Fatal(err)
			}
		}
	}
	create("orphan-companion", session.SessionKindSideChat)
	create("ordinary", "")
	create("live-companion", session.SessionKindSideChat)

	liveRef := session.SessionRef{HostID: "side-chat-sweep", SessionID: "live-companion"}
	binding, err := service.EnsureExecution(context.Background(), liveRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Release(context.Background()) })

	if !session.ProbeWriterHeld(filepath.Join(root, "live-companion")) {
		t.Fatal("test setup: a bound runtime must hold its writer lease")
	}
	if session.ProbeWriterHeld(filepath.Join(root, "orphan-companion")) {
		t.Fatal("test setup: an unopened companion must not hold a writer lease")
	}

	deleted, err := sweepSideChatOrphans(context.Background(), service)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want exactly the orphan", deleted)
	}
	for id, kept := range map[string]bool{"orphan-companion": false, "ordinary": true, "live-companion": true} {
		_, err := os.Stat(filepath.Join(root, id))
		if kept && err != nil {
			t.Errorf("%s was swept, want kept: %v", id, err)
		}
		if !kept && !os.IsNotExist(err) {
			t.Errorf("%s still on disk, want swept: %v", id, err)
		}
	}
}
