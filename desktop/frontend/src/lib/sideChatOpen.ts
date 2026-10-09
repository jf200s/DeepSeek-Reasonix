// Opening a companion session is a host round-trip followed by one dock tab, so
// it lives here rather than in the presentation-only dock region: the region
// keeps its props contract, and this module owns the bridge call.
import { useActivityBarStore } from "../store/activityBar";
import { app } from "./bridge";

/**
 * Opens a read-only companion session owned by parentTabId and registers the
 * dock tab that renders it. The label carries the companion ordinal (so closing
 * the first companion lets the next one reuse its number), and the tab meta
 * carries the parent/child ids the panel routes on.
 */
export async function openSideChatTab(parentTabId: string, title: string): Promise<void> {
  if (!parentTabId) return;
  const result = await app.OpenSideChatForTab(parentTabId);
  useActivityBarStore.getState().addTab("sideChat", `${title} ${result.Ordinal}`, {
    parentTabId,
    childTabId: result.TabID,
    ordinal: result.Ordinal,
  });
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
