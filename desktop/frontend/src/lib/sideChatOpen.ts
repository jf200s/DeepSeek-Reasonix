// Opening a companion session is a host round-trip followed by one dock tab, so
// it lives here rather than in the presentation-only dock region: the region
// keeps its props contract, and this module owns the bridge call.
import { useActivityBarStore } from "../store/activityBar";
import { app } from "./bridge";
import { prefillSideChatDraft } from "./sideChatDraft";

/**
 * Opens a read-only companion session owned by parentTabId and registers the
 * dock tab that renders it. The label carries the companion ordinal (so closing
 * the first companion lets the next one reuse its number), and the tab meta
 * carries the parent/child ids the panel routes on. Returns the child tab id so
 * a caller with something to hand over (a selection) can address it.
 */
export async function openSideChatTab(parentTabId: string, title: string): Promise<string> {
  if (!parentTabId) return "";
  const result = await app.OpenSideChatForTab(parentTabId);
  useActivityBarStore.getState().addTab("sideChat", `${title} ${result.Ordinal}`, {
    parentTabId,
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
 * The transcript selection action: brings up this owner's companion — reusing the
 * one already docked, otherwise opening a new one — and deposits the selected
 * text as that companion's composer draft. It never submits, because the user is
 * asking a question and still has to finish writing it. Returns false when the
 * host refused the open, e.g. because the owner is read-only and offers no
 * companion at all.
 */
export async function askInSideChat(parentTabId: string, text: string, title: string): Promise<boolean> {
  if (!parentTabId || !text.trim()) return false;
  const docked = dockedCompanionOf(parentTabId);
  if (docked) {
    useActivityBarStore.getState().activateTab(docked.id);
    prefillSideChatDraft(docked.childTabId, text);
    return true;
  }
  try {
    const childTabId = await openSideChatTab(parentTabId, title);
    if (!childTabId) return false;
    prefillSideChatDraft(childTabId, text);
    return true;
  } catch {
    return false;
  }
}

/** The companion dock tab this owner already has, if any. */
function dockedCompanionOf(parentTabId: string): { id: string; childTabId: string } | null {
  for (const tab of useActivityBarStore.getState().tabs) {
    if (tab.type !== "sideChat") continue;
    if (tab.meta?.parentTabId !== parentTabId) continue;
    const childTabId = tab.meta?.childTabId;
    if (typeof childTabId === "string" && childTabId) return { id: tab.id, childTabId };
  }
  return null;
}
