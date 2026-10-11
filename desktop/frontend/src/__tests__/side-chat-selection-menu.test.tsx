// Run: tsx src/__tests__/side-chat-selection-menu.test.tsx
//
// Contract for the transcript's selection action. The menu is a floating element
// portaled to <body>, so the transcript's own mouseup listener also sees clicks
// on the menu itself: reading that as a fresh selection unmounted the menu before
// its click landed, and the action silently never ran. jsdom cannot produce a
// real user selection, so the selection is stubbed and the mouseup path is driven
// directly.

import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";

const harness = await createTranscriptHarness();
try {
  const asked: string[] = [];
  await harness.render(
    [{ kind: "user", id: "u1", text: "why is the sky blue?" }],
    { onAskInSideChat: (text: string) => { asked.push(text); } },
  );
  await harness.settle();

  const scroll = harness.scrollElement();
  // The surface renders chat-node/msg markup rather than the windowed
  // transcript__row one; the selection anchor only has to sit inside the scroller.
  try {
    await harness.waitFor(() => scroll.querySelector(".chat-node") !== null, "a transcript row", 20);
  } catch (error) {
    console.error("transcript DOM:", scroll.innerHTML.slice(0, 900));
    throw error;
  }
  const row = scroll.querySelector(".chat-node");
  assert.ok(row, "the transcript rendered a row to select from");

  // jsdom cannot produce a real user selection: stub the one thing the action
  // reads, anchored on a node that really is inside the transcript.
  Object.defineProperty(harness.dom.window, "getSelection", {
    configurable: true,
    value: () => ({
      isCollapsed: false,
      anchorNode: row,
      focusNode: row,
      toString: () => "  the selected paragraph  ",
    }),
  });

  const mouseUpOn = (target: Element) => {
    target.dispatchEvent(new MouseEvent("mouseup", {
      bubbles: true, cancelable: true, clientX: 120, clientY: 340,
    }));
  };

  // A mouseup inside the transcript with a live selection proposes the action.
  await act(async () => { mouseUpOn(scroll); });
  await harness.waitFor(() => document.querySelector(".floating-menu") !== null, "the selection menu to open");
  assert.ok(document.querySelector(".floating-menu"), "a selection opens the action menu");

  // The menu is portaled to <body>, so its own mouseup lands outside the
  // transcript. That must not be read as a new selection: doing so closed the
  // menu before the click could land.
  const item = document.querySelector<HTMLButtonElement>(".floating-menu button");
  assert.ok(item, "the menu renders its action");
  await act(async () => { mouseUpOn(item); });
  await harness.flush();
  assert.ok(document.querySelector(".floating-menu"), "a mouseup on the menu itself keeps it open");

  // Clicking it hands the selection over and closes the menu.
  await act(async () => { item.click(); });
  await harness.flush();
  assert.equal(asked.length, 1, `expected the selection to be handed over, got ${JSON.stringify(asked)}`);
  assert.equal(asked[0], "the selected paragraph", "the action receives the trimmed selection");
  assert.equal(document.querySelector(".floating-menu"), null, "the menu closes once the action ran");
} finally {
  await harness.close();
}

console.log("side-chat selection menu: opens on a selection, survives its own mouseup, hands the text over");
