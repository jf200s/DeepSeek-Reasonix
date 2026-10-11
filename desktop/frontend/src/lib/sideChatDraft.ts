// A companion's composer is not wired to the main area's composer-insert
// commands: those are keyed by the active tab, and a companion is never the
// active main tab — the host creates its child tab and the dock panel owns it.
// This module is the small hand-off between the two: the selection action
// deposits text for one companion tab, and the panel takes it on mount, or
// subscribes while it is already open.
export type SideChatDraft = { id: number; text: string };

const pending = new Map<string, SideChatDraft>();
const waiting = new Map<string, Set<(draft: SideChatDraft) => void>>();
let nextDraftId = 0;

/**
 * Hands one selection to a companion tab's composer. A panel that is already
 * mounted takes it immediately; otherwise the draft waits until the panel
 * mounts, which is the common case right after the selection opened it.
 */
export function prefillSideChatDraft(tabId: string, text: string): void {
  const trimmed = text.trim();
  if (!tabId || !trimmed) return;
  const draft: SideChatDraft = { id: ++nextDraftId, text: trimmed };
  const listeners = waiting.get(tabId);
  if (listeners && listeners.size > 0) {
    for (const listener of listeners) listener(draft);
    return;
  }
  pending.set(tabId, draft);
}

/**
 * Subscribes to drafts for one companion tab while its panel is mounted. The
 * returned function unsubscribes; drafts deposited before the subscription are
 * still delivered by takeSideChatDraft on mount.
 */
export function subscribeSideChatDraft(tabId: string, listener: (draft: SideChatDraft) => void): () => void {
  let listeners = waiting.get(tabId);
  if (!listeners) {
    listeners = new Set();
    waiting.set(tabId, listeners);
  }
  listeners.add(listener);
  return () => {
    listeners?.delete(listener);
    if (listeners && listeners.size === 0) waiting.delete(tabId);
  };
}

/** Takes (and clears) the draft waiting for this companion tab, if any. */
export function takeSideChatDraft(tabId: string): SideChatDraft | null {
  const draft = pending.get(tabId) ?? null;
  pending.delete(tabId);
  return draft;
}
