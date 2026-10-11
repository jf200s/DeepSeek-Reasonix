package main

import (
	"context"
	"os"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

// sideChatCloseStubController implements only the slices of control.SessionAPI
// the companion close path reads. The embedded interfaces keep the rest of the
// (large) port satisfied without faking behaviour these tests never drive.
type sideChatCloseStubController struct {
	control.SessionAPI
	control.IdentityLifecycle
	service   *session.Service
	ref       session.SessionRef
	bound     bool
	exclusive bool
}

func (c *sideChatCloseStubController) RuntimeStatus() control.RuntimeStatus {
	return control.RuntimeStatus{}
}

func (c *sideChatCloseStubController) UsesExclusiveSession() bool { return c.exclusive }

func (c *sideChatCloseStubController) SessionRef() (session.SessionRef, bool) {
	return c.ref, c.bound
}

func (c *sideChatCloseStubController) SessionService() *session.Service { return c.service }

// plainTabStubController is a tab controller that is not an identity owner, so
// the companion close path must treat it as an ordinary tab.
type plainTabStubController struct {
	control.SessionAPI
}

// TestSideChatSessionTargetSkipsSharedAndUnboundSessions pins which sessions a
// companion close is allowed to delete: only the exclusive session a bound
// companion owns. A shared session belongs to its owner, and a companion that
// never bound one has nothing to remove — deleting either would reach into
// another tab's conversation.
func TestSideChatSessionTargetSkipsSharedAndUnboundSessions(t *testing.T) {
	root := t.TempDir()
	service, err := session.NewService("side-chat-delete", session.NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	ref := session.SessionRef{HostID: "side-chat-delete", SessionID: "companion-1"}

	cases := []struct {
		name        string
		ctrl        control.SessionAPI
		wantService bool
	}{
		{"a plain tab owns no companion session", &plainTabStubController{}, false},
		{"a shared session is left to its owner", &sideChatCloseStubController{service: service, ref: ref, bound: true}, false},
		{"an unbound companion has no ref to delete", &sideChatCloseStubController{service: service, ref: ref, exclusive: true}, false},
		{"an exclusive bound companion hands over its session", &sideChatCloseStubController{service: service, ref: ref, bound: true, exclusive: true}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gotRef, gotService := sideChatSessionTarget(&WorkspaceTab{ID: "child", Ctrl: testCase.ctrl})
			if testCase.wantService {
				if gotService != service {
					t.Fatalf("service = %v, want the companion's own service", gotService)
				}
				if gotRef != ref {
					t.Fatalf("ref = %+v, want %+v", gotRef, ref)
				}
				return
			}
			if gotService != nil || gotRef.SessionID != "" {
				t.Fatalf("target = (%+v, %v), want nothing deletable", gotRef, gotService)
			}
		})
	}
}

// TestDeleteSideChatSessionRemovesItsThrowawaySession covers the deletion the
// close path performs. The close itself needs a booted controller, so this
// drives the same call site against a real session service and insists the
// companion's session directory is gone: a companion is throwaway, and leaving
// its store behind after "close" is the leak the close warns about.
func TestDeleteSideChatSessionRemovesItsThrowawaySession(t *testing.T) {
	service, err := session.NewService("side-chat-delete", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runtime, err := service.Create(context.Background(), session.CreateOptions{
		SessionID: "companion-1",
		Kind:      session.SessionKindSideChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := runtime.Ref()
	t.Cleanup(func() { _ = service.Close(context.Background(), ref) })

	dir, err := service.SessionDir(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("companion session dir %q missing before the close: %v", dir, err)
	}

	app := &App{}
	if err := app.deleteSideChatSession("child", service, ref); err != nil {
		t.Fatalf("delete companion session: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("companion session dir %q survived the close (stat err = %v)", dir, err)
	}

	// A companion that bound no exclusive session has nothing to remove; that is
	// a warning, not a failed close.
	if err := app.deleteSideChatSession("child", nil, session.SessionRef{}); err != nil {
		t.Fatalf("a companion with no session to delete must not fail the close: %v", err)
	}
}
