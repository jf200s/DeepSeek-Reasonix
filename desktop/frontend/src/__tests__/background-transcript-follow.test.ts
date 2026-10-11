// Run: tsx src/__tests__/background-transcript-follow.test.ts
//
// Contract for the follower a tab the main area is not showing still needs. A
// dock companion is never the active main tab, and events reach a tab's state
// only through its follower, so the active-only ready/hydrate path dropped the
// companion's whole turn: the panel stayed on its empty hero while the host ran
// and persisted it. A background tab's readiness is not a navigation intent, and
// an already-attached tab must never be attached twice.

import { JSDOM } from "jsdom";
import type { State } from "../lib/useController";
import type { TranscriptSessionFollower } from "../lib/transcriptSessionFollower";

// The follower reads the shell through desktopHost(), so install both globals.
const dom = new JSDOM("", { url: "https://reasonix.local/" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.localStorage = dom.window.localStorage;

import { installDesktopHostStub } from "./desktopHostStub";

installDesktopHostStub({});

const { createBackgroundFollow } = await import("../lib/backgroundTranscriptFollow");

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

const subscribed: string[] = [];
const dispatched: string[] = [];
let states = new Map<string, State>();
const followers: { current: Map<string, TranscriptSessionFollower> } = { current: new Map() };

const follow = createBackgroundFollow({
  followers,
  state: (tabId) => states.get(tabId),
  subscribe: (tabId) => { subscribed.push(tabId); },
  dispatch: (tabId) => { dispatched.push(tabId); },
});

/** A tab state whose meta is ready and backed by a session path. */
function stateOf(top: Record<string, unknown> = {}, meta: Record<string, unknown> = {}): State {
  return { meta: { ready: true, sessionPath: "/tmp/companion", ...meta }, ...top } as unknown as State;
}

function reset(): void {
  subscribed.length = 0;
  dispatched.length = 0;
  states = new Map();
  followers.current.clear();
}

// --- a tab with no meta yet has nothing to follow ---
reset();
states.set("no-meta", {} as State);
follow.attach("no-meta");
follow.attachOnReady("no-meta");
check("a tab without meta is not attached", subscribed.length === 0, `subscribed=${JSON.stringify(subscribed)}`);

// --- a tab another path is already loading is left to that path ---
reset();
states.set("hydrating", stateOf({ hydrating: true }));
follow.attach("hydrating");
check("a hydrating tab is not attached", subscribed.length === 0, `subscribed=${JSON.stringify(subscribed)}`);

reset();
states.set("pending", stateOf({ backendActivationPending: true }));
follow.attach("pending");
check("a tab awaiting backend activation is not attached", subscribed.length === 0);

reset();
states.set("cold", stateOf({}, { ready: false }));
follow.attach("cold");
check("a tab needing a cold read is not attached", subscribed.length === 0);

// --- a ready background tab gets exactly one follower ---
reset();
states.set("companion", stateOf());
follow.attach("companion");
check("a ready background tab is attached", subscribed.length === 1 && subscribed[0] === "companion", `subscribed=${JSON.stringify(subscribed)}`);
check("the follower is registered for that tab", followers.current.has("companion"));

follow.attach("companion");
follow.attachOnReady("companion");
check("an attached tab is not attached twice", subscribed.length === 1, `subscribed=${JSON.stringify(subscribed)}`);

// --- readiness of a background tab attaches without navigating it ---
reset();
states.set("late", stateOf());
follow.attachOnReady("late");
check("readiness attaches a background tab", subscribed.length === 1 && subscribed[0] === "late", `subscribed=${JSON.stringify(subscribed)}`);
check("attaching on readiness does not dispatch a navigation", dispatched.length === 0, `dispatched=${JSON.stringify(dispatched)}`);

reset();
states.set("cold-late", stateOf({}, { ready: false }));
follow.attachOnReady("cold-late");
check("readiness that still needs a cold read is not attached", subscribed.length === 0);

// Let a started follower's rejected bridge call settle before the summary.
await new Promise((resolve) => setTimeout(resolve, 0));

console.log(`\nbackground transcript follow: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
