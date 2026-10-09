// Run: tsx src/__tests__/side-chat-dock-tab.test.ts
//
// Contract for the side-chat dock tab. It is a first-class dock tab type, but a
// companion session cannot outlive the app, so the tab is session-local: it
// never enters the persisted dock snapshot and a stale one is dropped on
// restore. Ordinary tabs keep round-tripping exactly as before.

import { JSDOM } from "jsdom";

// The store reads localStorage at module load, so install a jsdom global first.
const dom = new JSDOM("", { url: "https://reasonix.local/" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.localStorage = dom.window.localStorage;

const STORAGE_KEY = "reasonix.dock.tabs";
// Query-suffixed specifier: a fresh module instance re-reads localStorage, which
// is what restore actually does at app start. Held in a variable so tsc does not
// try to resolve it as a path.
const staleModuleSpecifier = "../store/activityBar?stale-snapshot";
const { useActivityBarStore } = await import("../store/activityBar");

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

type Snapshot = { tabs?: Array<{ id: string; type: string }>; activeTabId?: string | null };

function snapshot(): Snapshot {
  return JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}") as Snapshot;
}

// --- a side chat opens as a dock tab carrying its parent/child identity ---
reset();
useActivityBarStore.getState().addTab("sideChat", "辅助对话 1", { parentTabId: "parent-1", childTabId: "child-1", ordinal: 1 });
const opened = useActivityBarStore.getState().tabs;
check("side chat opens as a dock tab", opened.length === 1 && opened[0].type === "sideChat", `tabs=${JSON.stringify(opened)}`);
check("side chat keeps its parent/child meta", opened[0]?.meta?.parentTabId === "parent-1" && opened[0]?.meta?.childTabId === "child-1");
check("side chat becomes the active tab", useActivityBarStore.getState().activeTabId === opened[0]?.id);

// --- it never enters the persisted snapshot ---
check(
  "side chat is not written to the dock snapshot",
  (snapshot().tabs ?? []).every((tab) => tab.type !== "sideChat"),
  `snapshot=${localStorage.getItem(STORAGE_KEY)}`,
);

// --- while ordinary tabs still round-trip ---
useActivityBarStore.getState().addTab("file", "Files");
const withFiles = snapshot();
check("an ordinary tab still persists", (withFiles.tabs ?? []).some((tab) => tab.type === "file"), `snapshot=${localStorage.getItem(STORAGE_KEY)}`);
check("ordinary tabs do not carry the side chat along", (withFiles.tabs ?? []).every((tab) => tab.type !== "sideChat"));

// --- a stale snapshot that carries a side chat is dropped on restore ---
localStorage.setItem(STORAGE_KEY, JSON.stringify({
  tabs: [
    { id: "stale-side", type: "sideChat", label: "辅助对话 1", meta: { parentTabId: "p", childTabId: "c", ordinal: 1 } },
    { id: "files", type: "file", label: "Files" },
  ],
  activeTabId: "stale-side",
}));
const restored = await import(staleModuleSpecifier);
check(
  "restore drops a side chat that no session backs anymore",
  restored.useActivityBarStore.getState().tabs.every((tab: { type: string }) => tab.type !== "sideChat"),
  `tabs=${JSON.stringify(restored.useActivityBarStore.getState().tabs)}`,
);
check("restore keeps the ordinary tab", restored.useActivityBarStore.getState().tabs.some((tab: { type: string }) => tab.type === "file"));
check("restore re-anchors the active tab away from the dropped one", restored.useActivityBarStore.getState().activeTabId === "files");

console.log(`\nside-chat dock tab: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
