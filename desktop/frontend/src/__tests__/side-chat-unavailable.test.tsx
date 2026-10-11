// Run: tsx src/__tests__/side-chat-unavailable.test.tsx
//
// Contract for a refused companion: the dock's + menu cannot tell whether the
// owner is a read-only task, so the host's refusal is the only signal there is.
// It has to reach the user — before this the rejection was unhandled and the
// menu entry simply looked dead — and it must not leave a half-opened tab.

import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
class TestResizeObserver { observe() {} unobserve() {} disconnect() {} }
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  localStorage: dom.window.localStorage,
  IS_REACT_ACT_ENVIRONMENT: true,
  ResizeObserver: TestResizeObserver,
});
(dom.window as unknown as { ResizeObserver: unknown }).ResizeObserver = TestResizeObserver;

import { installDesktopHostStub } from "./desktopHostStub";

const refused: string[] = [];
installDesktopHostStub({
  // The read-only-owner refusal the host actually returns, mirrored here.
  OpenSideChatForTab: async (parentTabId: string) => {
    refused.push(parentTabId);
    throw new Error("side chat: read-only channel");
  },
});

const { WorkspaceDockRegion } = await import("../app-shell/WorkspaceDockRegion");
const { LocaleProvider } = await import("../lib/i18n");
const { ToastProvider } = await import("../lib/toast");
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

// The stub sends the locale through as the key itself, so a rendered sentence is
// observable as its key.
const props = {
  visible: true, overlay: false, mode: "files" as const, showContext: true,
  t: (key: string) => key,
  onPickEntry: () => {},
  remote: {} as never,
  context: {} as never,
  workspace: { tabId: "owner-1" } as never,
  workspaceKey: "k",
};

useActivityBarStore.setState({ tabs: [], activeTabId: null, addMenuOpen: false, recentlyClosed: [] });
const container = document.getElementById("root");
assert.ok(container, "harness root missing");
const root = createRoot(container);
const settle = () => act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });

await act(async () => {
  root.render(
    <LocaleProvider>
      <ToastProvider>
        <WorkspaceDockRegion {...(props as Parameters<typeof WorkspaceDockRegion>[0])} />
      </ToastProvider>
    </LocaleProvider>,
  );
});
await settle();

// The + menu is what a user actually picks; open it the way the + button does.
await act(async () => { useActivityBarStore.getState().setAddMenuOpen(true); });
await settle();
const items = [...document.querySelectorAll<HTMLButtonElement>(".tab-add-menu__item")];
// The menu translates through LocaleProvider, so the entry carries real copy
// rather than the test's key-through translator.
const sideChatItem = items.find((button) =>
  ["辅助对话", "Side conversation"].some((label) => button.textContent?.includes(label)));
check("the + menu offers a companion entry", Boolean(sideChatItem), `items=${JSON.stringify(items.map((i) => i.textContent))}`);

await act(async () => { sideChatItem?.click(); });
await settle();

check("the host was actually asked to open a companion", refused.length === 1 && refused[0] === "owner-1", `asked=${JSON.stringify(refused)}`);
const toast = document.querySelector(".toast__text")?.textContent ?? "";
check("a refused companion tells the user why", toast.includes("sideChat.unavailable"), `toast=${JSON.stringify(toast)}`);
check(
  "a refused companion leaves no dock tab behind",
  useActivityBarStore.getState().tabs.every((tab) => tab.type !== "sideChat"),
  `tabs=${JSON.stringify(useActivityBarStore.getState().tabs)}`,
);

await act(async () => root.unmount());
console.log(`\nside-chat unavailable: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
