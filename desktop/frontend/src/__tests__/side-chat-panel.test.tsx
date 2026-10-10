// Run: tsx src/__tests__/side-chat-panel.test.tsx
//
// Contract for the companion panel: it renders whichever session its child tab
// id points at (never the parent's), names the conversation that owns it,
// survives having no state yet, and forwards typed input to that child tab. The
// read-only part of the feature lives in the host's tool registry, not here —
// the user still has to be able to ask.

import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { installBridgeApp, installDom } from "./composerInboxHarness";

installDom();
// The composer asks the host for commands/models while it mounts. MetaForTab is
// how the panel asks for the owner tab's title: the owner is a main-area tab and
// never appears among the dock tabs, so the host is the only place to look. Any
// other tab has no meta at all — the same answer the panel's own child lookup
// gets, since nothing else in this test provides tab metadata.
installBridgeApp({
  Commands: async () => [],
  ModelsForTab: async () => [],
  SearchFileRefsForTab: async () => [],
  MetaForTab: async (tabID: string) =>
    tabID === "parent-1" ? { label: "OWNER_CONVERSATION" } : undefined,
});

const { SideChatPanel } = await import("../components/SideChatPanel");
const { LocaleProvider } = await import("../lib/i18n");
const { ToastProvider } = await import("../lib/toast");
const { getTranscriptStore } = await import("../lib/transcriptStore");
const { prefillSideChatDraft, takeSideChatDraft } = await import("../lib/sideChatDraft");
const { initialState } = await import("../lib/useController");

const submitted: Array<{ tabId: string; display: string; input: string }> = [];
const container = document.getElementById("root");
assert.ok(container, "harness root missing");
const root = createRoot(container);

const paint = async (ownerTabId = "parent-1") => {
  root.render(
    <LocaleProvider>
      <ToastProvider>
        <SideChatPanel
          tabId="child-1"
          parentTabId={ownerTabId}
          cwd="/repo"
          onSubmit={(tabId, display, input) => { submitted.push({ tabId, display, input }); }}
        />
      </ToastProvider>
    </LocaleProvider>,
  );
  // One macrotask: the owner-title round trip the panel awaits settles in it,
  // together with everything else the composer kicks off on mount.
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
};

await act(async () => { await paint(); });
assert.ok(container.querySelector('[data-side-chat-tab-id="child-1"]'), "the panel mounts before its session has state");

// A dock holding several companions has to say which conversation each one
// belongs to, so the panel asks the host for the owner tab's title and shows it.
const owner = container.querySelector('[data-side-chat-parent-id="parent-1"]');
assert.ok(owner, "the panel names the owner conversation");
assert.match(owner?.textContent ?? "", /OWNER_CONVERSATION/, `owner label=${owner?.textContent}`);

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

// The host implements no model switch, approval mode or effort for a companion,
// so the composer must not offer them: they would render live and do nothing.
// This is Composer's hostControls, driven from SideChatPanel.
assert.equal(container.querySelector(".modelsw"), null, "a companion offers no model switcher");
assert.equal(container.querySelector(".composer-meta__control--approval"), null, "a companion offers no approval mode");
assert.equal(container.querySelector(".composer-effort-control"), null, "a companion offers no effort control");

// An owner the host cannot describe leaves the panel with no label line at all,
// rather than an empty one: a titleless owner is not worth a stray "From".
await act(async () => { await paint("parent-unknown"); });
assert.ok(!container.querySelector('[data-side-chat-parent-id="parent-unknown"]'), "an undescribed owner adds no label line");
assert.ok(!container.querySelector(".side-chat-panel__parent"), "the previous owner's label does not linger");

await act(async () => root.unmount());

// A draft deposited before the panel mounts has to survive React's doubled
// effects: StrictMode runs the mount effect twice, and re-taking the draft on the
// second run leaves the composer empty, so the selection silently vanishes.
{
  const strictChildId = "child-strict";
  prefillSideChatDraft(strictChildId, "a question handed over before mount");
  const strictHost = document.createElement("div");
  document.body.appendChild(strictHost);
  const strictRoot = createRoot(strictHost);
  await act(async () => {
    strictRoot.render(
      <LocaleProvider>
        <ToastProvider>
          <React.StrictMode>
            <SideChatPanel tabId={strictChildId} parentTabId="parent-1" cwd="/repo" onSubmit={() => {}} />
          </React.StrictMode>
        </ToastProvider>
      </LocaleProvider>,
    );
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  assert.match(strictHost.textContent ?? "", /a question handed over before mount/, "a pre-mount draft survives StrictMode's doubled mount effect");
  assert.equal(takeSideChatDraft(strictChildId), null, "the draft is taken exactly once");
  await act(async () => strictRoot.unmount());
  strictHost.remove();
}

console.log("side-chat panel: renders the child session, names its owner, submits to it, and keeps a handed-over draft");
