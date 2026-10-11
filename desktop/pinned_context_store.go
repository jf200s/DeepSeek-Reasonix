package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/fileutil"
	"reasonix/internal/store"
)

const (
	pinnedContextSchemaVersion = 1
	maxPinnedContextStateBytes = 64 * 1024
)

type pinnedContextState struct {
	SchemaVersion int      `json:"schemaVersion"`
	SessionID     string   `json:"sessionId"`
	Files         []string `json:"files"`
}

func pinnedContextStateFor(owner string) pinnedContextState {
	return pinnedContextState{
		SchemaVersion: pinnedContextSchemaVersion,
		SessionID:     owner,
		Files:         []string{},
	}
}

func emptyPinnedContextState(sessionPath string) pinnedContextState {
	return pinnedContextStateFor(agent.BranchID(sessionPath))
}

// pinnedContextSidecarForSession is the sidecar of a canonical session. A
// canonical session intentionally carries no legacy transcript path, so its
// pinned context lives inside the v5 by-id directory beside its own records.
func pinnedContextSidecarForSession(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	root := config.DesktopSessionStoreDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, sessionID, store.PinnedContextSidecarName)
}

// pinnedContextSidecarFor resolves a session's sidecar from either identity, so
// callers that may hold a canonical session or a legacy one share one path.
func pinnedContextSidecarFor(sessionID, sessionPath string) string {
	if id := strings.TrimSpace(sessionID); id != "" {
		return pinnedContextSidecarForSession(id)
	}
	return store.SessionPinnedContext(sessionPath)
}

// pinnedContextOwner is the session identity a sidecar records and logs.
func pinnedContextOwner(sessionID, sessionPath string) string {
	if id := strings.TrimSpace(sessionID); id != "" {
		return id
	}
	return agent.BranchID(sessionPath)
}

func normalizePinnedContextFiles(files []string) ([]string, error) {
	out := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, path := range files {
		clean, err := normalizePinnedRelPath(path)
		if err != nil {
			return nil, fmt.Errorf("invalid pinned path %q: %w", path, err)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	if len(out) > maxPinnedFileCount {
		return nil, fmt.Errorf("at most %d files can be pinned", maxPinnedFileCount)
	}
	sort.Strings(out)
	return out, nil
}

// readPinnedContextSidecar decodes and validates one sidecar. A missing file
// yields the empty state; owner must match the recorded owner so a stale
// sidecar can never seed a different session.
func readPinnedContextSidecar(sidecar, owner string) (pinnedContextState, error) {
	state := pinnedContextStateFor(owner)
	if strings.TrimSpace(sidecar) == "" {
		return state, nil
	}
	file, err := os.Open(sidecar)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read pinned context state: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return state, fmt.Errorf("stat pinned context state: %w", err)
	}
	if info.Size() > maxPinnedContextStateBytes {
		return state, fmt.Errorf("pinned context state exceeds %d bytes", maxPinnedContextStateBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPinnedContextStateBytes+1))
	if err != nil {
		return state, fmt.Errorf("read pinned context state: %w", err)
	}
	if len(raw) > maxPinnedContextStateBytes {
		return state, fmt.Errorf("pinned context state exceeds %d bytes", maxPinnedContextStateBytes)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return pinnedContextStateFor(owner), fmt.Errorf("decode pinned context state: %w", err)
	}
	if state.SchemaVersion != pinnedContextSchemaVersion {
		return pinnedContextStateFor(owner), fmt.Errorf("unsupported pinned context schema version %d", state.SchemaVersion)
	}
	if state.SessionID != owner {
		return pinnedContextStateFor(owner), fmt.Errorf("pinned context belongs to session %q, not %q", state.SessionID, owner)
	}
	files, err := normalizePinnedContextFiles(state.Files)
	if err != nil {
		return pinnedContextStateFor(owner), err
	}
	state.Files = files
	return state, nil
}

// writePinnedContextSidecar persists one session's pinned files. It creates the
// owning directory, because a canonical session's by-id directory is not
// guaranteed to exist before its first pin.
func writePinnedContextSidecar(sidecar, owner string, files []string) error {
	if strings.TrimSpace(sidecar) == "" || strings.TrimSpace(owner) == "" {
		return fmt.Errorf("session is not ready")
	}
	normalized, err := normalizePinnedContextFiles(files)
	if err != nil {
		return err
	}
	state := pinnedContextStateFor(owner)
	state.Files = normalized
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxPinnedContextStateBytes {
		return fmt.Errorf("pinned context state exceeds %d bytes", maxPinnedContextStateBytes)
	}
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
		return fmt.Errorf("create pinned context directory: %w", err)
	}
	return fileutil.AtomicWriteFileStrict(sidecar, raw, 0o600)
}

// loadPinnedContextState reads a legacy session's sibling sidecar.
func loadPinnedContextState(sessionPath string) (pinnedContextState, error) {
	return readPinnedContextSidecar(store.SessionPinnedContext(sessionPath), agent.BranchID(sessionPath))
}

// savePinnedContextState writes a legacy session's sibling sidecar.
func savePinnedContextState(sessionPath string, files []string) error {
	return writePinnedContextSidecar(store.SessionPinnedContext(sessionPath), agent.BranchID(sessionPath), files)
}

// loadPinnedContextStateForSession reads a canonical session's v5 sidecar.
func loadPinnedContextStateForSession(sessionID string) (pinnedContextState, error) {
	return readPinnedContextSidecar(pinnedContextSidecarForSession(sessionID), strings.TrimSpace(sessionID))
}

// savePinnedContextStateForSession writes a canonical session's v5 sidecar.
func savePinnedContextStateForSession(sessionID string, files []string) error {
	return writePinnedContextSidecar(pinnedContextSidecarForSession(sessionID), strings.TrimSpace(sessionID), files)
}

// pinnedContextLoad reads whichever sidecar the given identity owns.
func pinnedContextLoad(sessionID, sessionPath string) (pinnedContextState, error) {
	if id := strings.TrimSpace(sessionID); id != "" {
		return loadPinnedContextStateForSession(id)
	}
	return loadPinnedContextState(sessionPath)
}

// pinnedContextSave writes whichever sidecar the given identity owns.
func pinnedContextSave(sessionID, sessionPath string, files []string) error {
	if id := strings.TrimSpace(sessionID); id != "" {
		return savePinnedContextStateForSession(id, files)
	}
	return savePinnedContextState(sessionPath, files)
}

func loadOrMigratePinnedContextState(sessionPath string, legacy []string) (pinnedContextState, error) {
	if strings.TrimSpace(sessionPath) == "" {
		state := emptyPinnedContextState("")
		files, err := normalizePinnedContextFiles(legacy)
		state.Files = files
		return state, err
	}
	state, err := loadPinnedContextState(sessionPath)
	if err != nil || len(state.Files) > 0 || len(legacy) == 0 {
		return state, err
	}
	if _, statErr := os.Stat(store.SessionPinnedContext(sessionPath)); statErr == nil {
		return state, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return state, statErr
	}
	files, err := normalizePinnedContextFiles(legacy)
	if err != nil {
		return state, err
	}
	if err := savePinnedContextState(sessionPath, files); err != nil {
		return state, err
	}
	state.Files = files
	return state, nil
}

func copyPinnedContextState(sourcePath, targetPath string) error {
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(targetPath) == "" {
		return nil
	}
	if _, err := os.Stat(store.SessionPinnedContext(sourcePath)); errors.Is(err, os.ErrNotExist) {
		return savePinnedContextState(targetPath, []string{})
	} else if err != nil {
		return err
	}
	state, err := loadPinnedContextState(sourcePath)
	if err != nil {
		return err
	}
	return savePinnedContextState(targetPath, state.Files)
}

func pinnedContextStateOrEmpty(sessionID, sessionPath, logMessage string) pinnedContextState {
	state, err := pinnedContextLoad(sessionID, sessionPath)
	if err == nil {
		return state
	}
	slog.Warn(logMessage, "session", pinnedContextOwner(sessionID, sessionPath), "err", err)
	return pinnedContextStateFor(pinnedContextOwner(sessionID, sessionPath))
}

func prepareStartupPinnedContext(tab *WorkspaceTab, startupPath, persistedPath string) {
	if startupPath != "" {
		migratePendingLegacyPinnedFiles(tab, tab.SessionID, startupPath)
		if len(tab.pendingLegacyPinnedFilesForPersistence()) > 0 {
			return
		}
		state := pinnedContextStateOrEmpty(tab.SessionID, startupPath, "desktop: load startup pinned context")
		tab.setPinnedFiles(state.Files)
	} else if strings.TrimSpace(persistedPath) != "" {
		// A rejected persisted path must not seed its replacement. A pathless
		// legacy entry keeps its cache until the one-time migration runs.
		tab.setPinnedFiles(nil)
	}
}

func restoreTabPinnedContext(tab *WorkspaceTab, legacy []string) {
	// Canonical identities and rejected locators must never enter the legacy
	// sidecar migration. A canonical session reads its own v5 sidecar; upgrade
	// input stays pending until a verified binding owns the migration.
	if tab.SessionID != "" {
		state, err := loadPinnedContextStateForSession(tab.SessionID)
		if err != nil {
			tab.retainLegacyPinnedFiles(legacy)
			slog.Warn("desktop: restore canonical pinned context", "session", tab.SessionID, "err", err)
			return
		}
		if len(legacy) > 0 {
			tab.setPinnedFilesState(state.Files, legacy)
			return
		}
		tab.setPinnedFiles(state.Files)
		return
	}
	path := ""
	if tab.SessionPath != "" {
		validated, ok := validatedLegacySessionPathForRead(tab.SessionPath)
		if !ok {
			tab.retainLegacyPinnedFiles(legacy)
			return
		}
		path = string(validated)
	}
	state, err := loadOrMigratePinnedContextState(path, legacy)
	if err != nil {
		tab.retainLegacyPinnedFiles(legacy)
		slog.Warn("desktop: restore pinned context", "err", err)
		return
	}
	if strings.TrimSpace(tab.SessionPath) == "" && len(legacy) > 0 {
		tab.setPinnedFilesState(state.Files, legacy)
		return
	}
	tab.setPinnedFiles(state.Files)
}

func migratePendingLegacyPinnedFiles(tab *WorkspaceTab, sessionID, sessionPath string) {
	legacy := tab.pendingLegacyPinnedFilesForPersistence()
	sidecar := pinnedContextSidecarFor(sessionID, sessionPath)
	if len(legacy) == 0 || strings.TrimSpace(sidecar) == "" {
		return
	}
	if _, err := os.Stat(sidecar); err == nil {
		if _, loadErr := pinnedContextLoad(sessionID, sessionPath); loadErr == nil {
			tab.clearPendingLegacyPinnedFiles()
		}
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := pinnedContextSave(sessionID, sessionPath, legacy); err != nil {
		slog.Warn("desktop: migrate pending legacy pinned context", "err", err)
		return
	}
	tab.clearPendingLegacyPinnedFiles()
}

// pinnedContextStateForSessionBinding loads the pinned state a tab adopts when
// it binds a session, and reports whether pending upgrade input must survive.
// Canonical bindings read the v5 sidecar; legacy bindings read the sibling file.
func pinnedContextStateForSessionBinding(tab *WorkspaceTab, sessionID, sessionPath string) (pinnedContextState, bool) {
	pendingLegacy := tab.pendingLegacyPinnedFilesForPersistence()
	_, sidecarErr := os.Stat(pinnedContextSidecarFor(sessionID, sessionPath))
	state, err := pinnedContextLoad(sessionID, sessionPath)
	if err != nil {
		slog.Warn("desktop: load session pinned context", "session", pinnedContextOwner(sessionID, sessionPath), "err", err)
	}
	preserveLegacy := len(pendingLegacy) > 0 && (errors.Is(sidecarErr, os.ErrNotExist) || err != nil)
	return state, preserveLegacy
}

func applyPinnedContextSessionBinding(tab *WorkspaceTab, state pinnedContextState, preserveLegacy bool) {
	if !preserveLegacy {
		tab.setPinnedFiles(state.Files)
	}
}
