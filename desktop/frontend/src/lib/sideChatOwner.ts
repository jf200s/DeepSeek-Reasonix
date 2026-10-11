// Which conversation a companion belongs to. Kept apart from sideChatOpen so the
// dock region can apply the rule without importing the host bridge: the region is
// presentation-only, and pulling the bridge in would drag it into the dock's
// import graph.
import type { TabItem } from "../store/activityBar";

/**
 * The conversation a companion belongs to. A companion cannot be keyed by its
 * parent tab alone: switching sessions inside the same main tab keeps the tab id
 * and only changes its session, so a tab-keyed owner would follow the wrong
 * conversation — a selection made in the new session would land in the old
 * session's companion. Both halves are recorded, and the conversation decides
 * which companion is shown and which one a selection belongs to.
 */
export type SideChatOwner = {
  /** Main-area tab that opened the companion. */
  tabId: string;
  /** The conversation that tab carried when the companion was opened. */
  sessionId: string;
};

/**
 * Whether a dock tab belongs to the given conversation. Ordinary tabs are always
 * docked; a companion belongs only to the session that opened it, so switching
 * the main tab to another session hides that companion from the dock and
 * switching back brings it out again. The dock's visibility filter and the
 * selection's owner match both rest on the same rule, so they cannot drift apart.
 */
export function dockTabBelongsToSession(tab: TabItem, sessionId: string): boolean {
  return tab.type !== "sideChat" || tab.meta?.parentSessionId === sessionId;
}
