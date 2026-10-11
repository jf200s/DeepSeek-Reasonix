// Run: tsx src/__tests__/side-chat-open.test.ts
//
// Contract for opening and closing a companion session from the dock. The + menu
// entry is not a local panel: the host must open the read-only child session
// first, and the dock tab is registered with the identity and ordinal the host
// returned. Closing goes through the host teardown before the tab disappears, so
// a failed teardown never makes the companion silently vanish.

import { JSDOM } from "jsdom";

// The store reads localStorage at module load, so install a jsdom global first.
const dom = new JSDOM("", { url: "https://reasonix.local/" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.localStorage = dom.window.localStorage;

import { installDesktopHostStub } from "./desktopHostStub";

const opened: string[] = [];
const closed: string[] = [];
// The bridge resolves bound commands from the host's live contract, so the
// companion commands are exercised through the real preload path (host.invoke)
// instead of the browser mock.
installDesktopHostStub({
  OpenSideChatForTab: async (parentTabId: string) => {
    opened.push(parentTabId);
    return { TabID: "child-7", SessionID: "session-7", Ordinal: 2 };
  },
  CloseSideChatTab: async (tabID: string) => {
    closed.push(tabID);
  },
});

const { useActivityBarStore } = await import("../store/activityBar");
const { openSideChatTab, closeSideChatTab } = await import("../lib/sideChatOpen");

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

// --- opening asks the host for the companion, then registers its dock tab ---
reset();
await openSideChatTab("parent-1", "辅助对话");
const tabs = useActivityBarStore.getState().tabs;
check("the host is asked for the parent tab", opened.length === 1 && opened[0] === "parent-1", `opened=${JSON.stringify(opened)}`);
check("the companion opens as a side-chat dock tab", tabs.length === 1 && tabs[0]?.type === "sideChat", `tabs=${JSON.stringify(tabs)}`);
check("the label carries the host ordinal", tabs[0]?.label === "辅助对话 2", `label=${tabs[0]?.label}`);
check(
  "the tab carries the parent/child ids and ordinal",
  tabs[0]?.meta?.parentTabId === "parent-1" && tabs[0]?.meta?.childTabId === "child-7" && tabs[0]?.meta?.ordinal === 2,
  `meta=${JSON.stringify(tabs[0]?.meta)}`,
);

// --- without a parent tab there is nothing to ask the host for ---
reset();
opened.length = 0;
await openSideChatTab("", "辅助对话");
check("no parent tab means no host call and no dock tab", opened.length === 0 && useActivityBarStore.getState().tabs.length === 0);

// --- closing tears the session down through the host, then drops the tab ---
reset();
await openSideChatTab("parent-1", "辅助对话");
const dockTab = useActivityBarStore.getState().tabs[0];
await closeSideChatTab(dockTab.id);
check("closing reaches the child session", closed.length === 1 && closed[0] === "child-7", `closed=${JSON.stringify(closed)}`);
check("closing removes the dock tab", useActivityBarStore.getState().tabs.length === 0);

console.log(`\nside-chat open: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
