// Run: tsx src/__tests__/side-chat-panel.test.tsx
//
// Contract for the companion panel: it renders whichever session its child tab
// id points at (never the parent's), survives having no state yet, and forwards
// typed input to that child tab. The read-only part of the feature lives in the
// host's tool registry, not here — the user still has to be able to ask.

import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { installBridgeApp, installDom } from "./composerInboxHarness";

installDom();
// The composer asks the host for commands/models while it mounts.
installBridgeApp({
  Commands: async () => [],
  ModelsForTab: async () => [],
  SearchFileRefsForTab: async () => [],
});

const { SideChatPanel } = await import("../components/SideChatPanel");
const { LocaleProvider } = await import("../lib/i18n");
const { ToastProvider } = await import("../lib/toast");
const { getTranscriptStore } = await import("../lib/transcriptStore");
const { initialState } = await import("../lib/useController");

const submitted: Array<{ tabId: string; display: string; input: string }> = [];
const container = document.getElementById("root");
assert.ok(container, "harness root missing");
const root = createRoot(container);

const paint = async () => {
  root.render(
    <LocaleProvider>
      <ToastProvider>
        <SideChatPanel
          tabId="child-1"
          parentTabId="parent-1"
          cwd="/repo"
          onSubmit={(tabId, display, input) => { submitted.push({ tabId, display, input }); }}
        />
      </ToastProvider>
    </LocaleProvider>,
  );
  await act(async () => { await Promise.resolve(); });
};

await act(async () => { await paint(); });
assert.ok(container.querySelector('[data-side-chat-tab-id="child-1"]'), "the panel mounts before its session has state");

// The transcript comes from the child tab's own slot in the per-tab store. The
// parent's text must never leak into the companion, and vice versa.
await act(async () => {
  const store = getTranscriptStore();
  store.setState("parent-1", { ...initialState, items: [{ kind: "user", id: "p-u1", text: "PARENT_SESSION_TEXT" }] });
  store.setState("child-1", { ...initialState, items: [
    { kind: "user", id: "c-u1", text: "COMPANION_QUESTION" },
    { kind: "assistant", id: "c-a1", text: "COMPANION_ANSWER", reasoning: "", streaming: false },
  ] });
  await paint();
});

const text = container.textContent ?? "";
assert.match(text, /COMPANION_ANSWER/, "the companion renders its own session");
assert.doesNotMatch(text, /PARENT_SESSION_TEXT/, "the panel never renders the parent session's transcript");

// Typing + submitting goes to the child tab, not to the dock's own tab id.
const textarea = container.querySelector("textarea");
assert.ok(textarea, "the composer mounts inside the panel");
// Paste through the composer's own handler: a controlled textarea ignores a
// direct value assignment, and this mirrors how the existing composer tests type.
await act(async () => {
  textarea.focus();
  textarea.setSelectionRange(0, textarea.value.length);
  const paste = new window.Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(paste, "clipboardData", {
    configurable: true,
    value: {
      files: [],
      items: [],
      types: ["text/plain"],
      getData: (kind: string) => (kind === "text" || kind === "text/plain" ? "why is this red?" : ""),
    },
  });
  textarea.dispatchEvent(paste);
  await new Promise((resolve) => setTimeout(resolve, 0));
});
const send = container.querySelector<HTMLButtonElement>(".composer__btn--send");
assert.ok(send, "the composer renders a send button");
await act(async () => {
  send.click();
});
assert.equal(submitted.length, 1, `expected one submission, got ${JSON.stringify(submitted)}`);
assert.equal(submitted[0]?.tabId, "child-1", "input is submitted to the companion session");
assert.match(submitted[0]?.input ?? "", /why is this red\?/);

await act(async () => root.unmount());
console.log("side-chat panel: renders the child session and submits to it");
