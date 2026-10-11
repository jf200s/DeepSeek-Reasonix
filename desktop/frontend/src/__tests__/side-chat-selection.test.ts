// Run: tsx src/__tests__/side-chat-selection.test.ts
//
// Contract for the transcript selection action: selecting text in the main
// conversation offers to ask in that owner's companion, and the selected text
// lands in the companion's composer instead of being sent. Three rules matter as
// much as the happy path: an already docked companion of the *same conversation*
// is reused rather than duplicated, a companion of another conversation is never
// reused (switching sessions inside one tab keeps the tab id, so the conversation
// is what decides), and a refused open hands nothing over at all.

import { JSDOM } from "jsdom";

// The store reads localStorage at module load, so install a jsdom global first.
const dom = new JSDOM("", { url: "https://reasonix.local/" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.localStorage = dom.window.localStorage;

import { installDesktopHostStub } from "./desktopHostStub";

const opened: string[] = [];
let refuseOpen = false;
// The companion commands resolve through the host's live contract, so they are
// exercised through the real preload path (host.invoke) like the dock commands.
installDesktopHostStub({
  OpenSideChatForTab: async (parentTabId: string) => {
    if (refuseOpen) throw new Error("side chat: read-only channel");
    opened.push(parentTabId);
    return { TabID: `child-${opened.length}`, SessionID: `session-${opened.length}`, Ordinal: opened.length };
  },
  CloseSideChatTab: async () => {},
});

const { useActivityBarStore } = await import("../store/activityBar");
const { askInSideChat, openSideChatTab } = await import("../lib/sideChatOpen");
const { prefillSideChatDraft, subscribeSideChatDraft, takeSideChatDraft } = await import("../lib/sideChatDraft");

let passed = 0;
let failed = 0;

function check(name: string, condition: boolean, detail = ""): void {
  if (condition) {
    passed += 1;
    console.log(`  PASS  ${name}`);
  } else {
    failed += 1;
    console.error(`  FAIL  ${name}${detail ? ` — ${detail}` : ""}`);
  }
}

function reset(): void {
  localStorage.clear();
  useActivityBarStore.setState({ tabs: [], activeTabId: null, addMenuOpen: false, recentlyClosed: [] });
}

// --- the selection brings up the owner's companion and hands the text over ---
reset();
opened.length = 0;
const handed = await askInSideChat({ tabId: "parent-1", sessionId: "session-1" }, "  the selected paragraph  ", "辅助对话");
const firstTabs = useActivityBarStore.getState().tabs;
check("the selection asks the host for the owner's companion", handed && opened.length === 1 && opened[0] === "parent-1", `opened=${JSON.stringify(opened)}`);
check("the companion opens as a dock tab", firstTabs.length === 1 && firstTabs[0]?.type === "sideChat", `tabs=${JSON.stringify(firstTabs)}`);
check("the dock tab carries the child id the text was addressed to", firstTabs[0]?.meta?.childTabId === "child-1", `meta=${JSON.stringify(firstTabs[0]?.meta)}`);
check("the dock tab records the owning conversation", firstTabs[0]?.meta?.parentSessionId === "session-1", `meta=${JSON.stringify(firstTabs[0]?.meta)}`);
check("the selected text becomes the companion's draft, trimmed", takeSideChatDraft("child-1")?.text === "the selected paragraph", `draft=${JSON.stringify(takeSideChatDraft("child-1"))}`);
check("the action leaves the dock tab active for the user", useActivityBarStore.getState().activeTabId === firstTabs[0]?.id);

// --- a companion that is already docked is reused, never duplicated ---
reset();
opened.length = 0;
await openSideChatTab({ tabId: "parent-2", sessionId: "session-2" }, "辅助对话");
await askInSideChat({ tabId: "parent-2", sessionId: "session-2" }, "what does this function do", "辅助对话");
const reusedTabs = useActivityBarStore.getState().tabs;
check("an already docked companion is reused", opened.length === 1 && reusedTabs.length === 1, `opened=${JSON.stringify(opened)} tabs=${reusedTabs.length}`);
check("the reused companion receives the draft", takeSideChatDraft("child-1")?.text === "what does this function do");

// --- another owner's companion is not reused for this one ---
reset();
opened.length = 0;
await openSideChatTab({ tabId: "owner-a", sessionId: "session-a" }, "辅助对话");
await askInSideChat({ tabId: "owner-b", sessionId: "session-b" }, "a different question", "辅助对话");
check("a companion of another owner does not absorb the selection", opened.length === 2 && useActivityBarStore.getState().tabs.length === 2, `opened=${JSON.stringify(opened)}`);
check("the new owner's companion gets its own draft", takeSideChatDraft("child-2")?.text === "a different question");

// --- the same tab in another session is another owner ---
// Switching sessions inside one main tab keeps the tab id and only changes the
// session, so the previous session's companion must not absorb this selection.
reset();
opened.length = 0;
await openSideChatTab({ tabId: "tab-1", sessionId: "session-x" }, "辅助对话");
await askInSideChat({ tabId: "tab-1", sessionId: "session-y" }, "asked after switching sessions", "辅助对话");
check(
  "a selection in another session does not reuse the previous session's companion",
  opened.length === 2 && useActivityBarStore.getState().tabs.length === 2,
  `opened=${JSON.stringify(opened)} tabs=${useActivityBarStore.getState().tabs.length}`,
);
check("the new session's companion receives the draft", takeSideChatDraft("child-2")?.text === "asked after switching sessions");
check("the previous session's companion keeps its composer empty", takeSideChatDraft("child-1") === null);
check(
  "each companion stays bound to its own conversation",
  useActivityBarStore.getState().tabs[0]?.meta?.parentSessionId === "session-x"
    && useActivityBarStore.getState().tabs[1]?.meta?.parentSessionId === "session-y",
  `metas=${JSON.stringify(useActivityBarStore.getState().tabs.map((tab) => tab.meta?.parentSessionId))}`,
);

// --- with two companions for one conversation, the one being read wins ---
reset();
opened.length = 0;
await openSideChatTab({ tabId: "parent-4", sessionId: "session-4" }, "辅助对话");
await openSideChatTab({ tabId: "parent-4", sessionId: "session-4" }, "辅助对话");
const pair = useActivityBarStore.getState().tabs;
check("one owner can hold two companions", pair.length === 2, `tabs=${pair.length}`);

// addTab leaves the newest active, so this selection belongs to the second one.
await askInSideChat({ tabId: "parent-4", sessionId: "session-4" }, "asked while reading the second", "辅助对话");
check("neither companion is duplicated for the same owner", opened.length === 2, `opened=${JSON.stringify(opened)}`);
check("the selection lands in the companion the user is reading", takeSideChatDraft("child-2")?.text === "asked while reading the second");
check("the companion the user is not reading keeps its composer empty", takeSideChatDraft("child-1") === null);

// Switching back moves the next selection with it.
useActivityBarStore.getState().activateTab(pair[0].id);
await askInSideChat({ tabId: "parent-4", sessionId: "session-4" }, "asked while reading the first", "辅助对话");
check("the preference follows the active tab, not the array order", takeSideChatDraft("child-1")?.text === "asked while reading the first");

// An active tab that is not this owner's companion falls back to the earliest.
await openSideChatTab({ tabId: "owner-c", sessionId: "session-c" }, "辅助对话");
await askInSideChat({ tabId: "parent-4", sessionId: "session-4" }, "asked from somewhere else", "辅助对话");
check("an unrelated active tab falls back to the earliest companion", takeSideChatDraft("child-1")?.text === "asked from somewhere else");
check("the fallback does not leak into the newer companion", takeSideChatDraft("child-2") === null);

// --- a refused open hands nothing over ---
reset();
opened.length = 0;
refuseOpen = true;
const refused = await askInSideChat({ tabId: "parent-3", sessionId: "session-3" }, "text nobody can ask about", "辅助对话");
refuseOpen = false;
check("a refused open reports false", refused === false);
check("a refused open leaves no dock tab", useActivityBarStore.getState().tabs.length === 0);
check("a refused open leaves no draft behind", takeSideChatDraft("child-1") === null);

// --- an open panel takes the draft by subscription, a closed one keeps it ---
reset();
const streamed: string[] = [];
const unsubscribe = subscribeSideChatDraft("child-live", (draft) => streamed.push(draft.text));
prefillSideChatDraft("child-live", "arrived while the panel is open");
check("an open panel receives the draft by subscription", streamed.length === 1 && streamed[0] === "arrived while the panel is open", `seen=${JSON.stringify(streamed)}`);
check("a delivered draft is not also left pending", takeSideChatDraft("child-live") === null);
unsubscribe();
prefillSideChatDraft("child-live", "waits for the panel to mount");
check("a draft deposited before mount is kept for it", takeSideChatDraft("child-live")?.text === "waits for the panel to mount");
prefillSideChatDraft("child-live", "   ");
check("a blank selection deposits nothing", takeSideChatDraft("child-live") === null);

console.log(`\nside-chat selection: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
