package main

import (
	"context"
	"log/slog"

	"reasonix/internal/session"
)

// sweepOrphanSideChatSessions clears companion stores a dead process left
// behind. A companion tab is never restored and a live companion always holds
// its writer lease, so at startup every unheld side-chat store is debris — from
// a crash, a kill, or a plain quit — and there is nothing else it could be.
func (a *App) sweepOrphanSideChatSessions() {
	service := a.desktopSessionService("")
	if service == nil {
		return
	}
	deleted, err := sweepSideChatOrphans(a.bootContext(), service)
	if err != nil {
		slog.Warn("desktop: orphan companion sweep unavailable", "err", err)
		return
	}
	if deleted > 0 {
		slog.Info("desktop: swept orphan companion sessions", "count", deleted)
	}
}

// sweepSideChatOrphans deletes each unheld companion store through the local
// session service. Split from the startup hook like deleteSideChatSession so a
// test can drive it against a real session service.
func sweepSideChatOrphans(ctx context.Context, service *session.Service) (int, error) {
	if service == nil {
		return 0, nil
	}
	infos, err := listAllCanonicalSessionInfo(ctx, service.Query())
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, info := range infos {
		if info.Kind != session.SessionKindSideChat {
			continue
		}
		// The probe answers for this process and a foreign one alike; a held
		// writer is a live companion and stays.
		if session.ProbeWriterHeld(info.Path) {
			continue
		}
		if err := service.Delete(ctx, info.Ref); err != nil {
			slog.Warn("desktop: orphan companion could not be deleted", "session", info.SessionID, "err", err)
			continue
		}
		deleted++
	}
	return deleted, nil
}
