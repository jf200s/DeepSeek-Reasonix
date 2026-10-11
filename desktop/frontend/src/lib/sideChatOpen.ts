// Opening a companion session is a host round-trip followed by one dock tab, so
// it lives here rather than the presentation-only dock region: the region
// keeps its props contract, and this module owns the bridge call.
import { useActivityBarStore } from "../store/activityBar";
import { app } from "./bridge";
import { prefillSideChatDraft } from "./sideChatDraft";
import type { SideChatOwner } from "./sideChatOwner";

/**
 * Opens a read-only companion session owned by the given conversation and
 * registers the dock tab that renders it. The label carries the companion
 * ordinal (so closing the first companion lets the next one reuse its number),
 * and the tab meta carries the parent/child ids and the owning conversation so a
 * session switch can tell which companion belongs to where. Returns the child
 * tab id so a caller with something to hand over (a selection) can address it.
 */
export async function openSideChatTab(owner: SideChatOwner, title: string): Promise<string> {
  if (!owner.tabId) return "";
  const result = await app.OpenSideChatForTab(owner.tabId);
  useActivityBarStore.getState().addTab("sideChat", `${title} ${result.Ordinal}`, {
    parentTabId: owner.tabId,
    parentSessionId: owner.sessionId,
    childTabId: result.TabID,
    ordinal: result.Ordinal,
  });
  return result.TabID;
}

/**
 * Closes one companion dock tab. The host owns the session teardown, so the tab
 * is only removed after CloseSideChatTab succeeds; a failure leaves the tab in
 * place so the user can retry instead of losing the companion silently.
 */
export async function closeSideChatTab(dockTabId: string): Promise<void> {
  const activity = useActivityBarStore.getState();
  const tab = activity.tabs.find((candidate) => candidate.id === dockTabId);
  const childTabId = typeof tab?.meta?.childTabId === "string" ? tab.meta.childTabId : "";
  if (!childTabId) {
    activity.closeTab(dockTabId);
    return;
  }
  await app.CloseSideChatTab(childTabId);
  useActivityBarStore.getState().closeTab(dockTabId);
}

/**
 * The transcript selection action: brings up this conversation's companion —
 * reusing the one already docked, otherwise opening a new one — and deposits the
 * selected text as that companion's composer draft. It never submits, because the
 * user is asking a question and still has to finish writing it. Returns false when
 * the host refused the open, e.g. because the owner is read-only and offers no
 * companion at all.
 */
export async function askInSideChat(owner: SideChatOwner, text: string, title: string): Promise<boolean> {
  if (!owner.tabId || !text.trim()) return false;
  const docked = dockedCompanionOf(owner);
  if (docked) {
    useActivityBarStore.getState().activateTab(docked.id);
    prefillSideChatDraft(docked.childTabId, text, title);
    return true;
  }
  try {
    const childTabId = await openSideChatTab(owner, title);
    if (!childTabId) return false;
    prefillSideChatDraft(childTabId, text, title);
    return true;
  } catch {
    return false;
  }
}

/**
 * The companion dock tab this conversation already has, if any. Only a companion
 * owned by the same conversation counts: a selection made after the main tab
 * switched sessions must not land in the previous session's companion, which is
 * still docked but belongs elsewhere. The one the user is looking at wins — a
 * selection made while reading a companion belongs in that companion, not in
 * whichever one happens to be oldest. Everything else falls back to the earliest
 * (tabs stay in creation order) so the choice is deterministic rather than an
 * accident of how the array is walked.
 */
function dockedCompanionOf(owner: SideChatOwner): { id: string; childTabId: string } | null {
  const { tabs, activeTabId } = useActivityBarStore.getState();
  let earliest: { id: string; childTabId: string } | null = null;
  for (const tab of tabs) {
    if (tab.type !== "sideChat") continue;
    if (tab.meta?.parentTabId !== owner.tabId) continue;
    if (tab.meta?.parentSessionId !== owner.sessionId) continue;
    const childTabId = tab.meta?.childTabId;
    if (typeof childTabId !== "string" || !childTabId) continue;
    if (tab.id === activeTabId) return { id: tab.id, childTabId };
    if (!earliest) earliest = { id: tab.id, childTabId };
  }
  return earliest;
}
