// Transcript followers for tabs the main area is not showing.
//
// Events reach a tab's state only through its TranscriptSessionFollower, and the
// ready/hydrate path covers the active tab alone. A dock companion is never the
// active main tab, so its events arrived at the renderer and were then dropped:
// the panel stayed on its empty hero while the host ran and persisted the turn.
// A background tab's readiness is not a navigation intent, so attaching a
// follower here never navigates.

import { needsColdHistory } from "./controllerHistoryMeta";
import { sessionIdentityStableKey } from "./sessionIdentity";
import { TranscriptSessionFollower } from "./transcriptSessionFollower";
import type { Action, State } from "./useController";

export type BackgroundFollowOptions = {
  /** Live per-tab followers, owned by the controller. */
  followers: { current: Map<string, TranscriptSessionFollower> };
  /** Reads the tab's current state; a companion's meta lands after creation. */
  state: (tabId: string) => State | undefined;
  /** Subscribes the tab's state to transcript-store patches. */
  subscribe: (tabId: string, binding: { path: string; key: string }) => void;
  dispatch: (tabId: string, action: Action) => void;
};

export type BackgroundFollow = {
  /** Starts (or restarts) a tab's follower and returns its metrics. */
  start: (tabId: string, path: string) => Promise<{ entries: number; inlineBytes: number }>;
  /** True when a tab that is not the active main tab still needs a follower. */
  eligible: (tabId: string) => boolean;
  /** Attaches on the readiness of a tab the main area is not showing. */
  attachOnReady: (tabId: string) => void;
  /** Per-event backstop: attaches as soon as a background tab's meta lands. */
  attach: (tabId: string) => void;
};

export function createBackgroundFollow(options: BackgroundFollowOptions): BackgroundFollow {
  const { followers, state, subscribe, dispatch } = options;

  // A tab that is hydrating, waiting on backend activation, or needs a cold
  // history read is already being loaded by that path, and one that has a
  // follower is attached. Everything else — the dock's companion — needs one.
  const eligible = (tabId: string) => {
    const current = state(tabId);
    if (!current?.meta || current.hydrating || current.backendActivationPending) return false;
    return !needsColdHistory(current.meta) && !followers.current.has(tabId);
  };

  const start: BackgroundFollow["start"] = async (tabId, path) => {
    subscribe(tabId, { path, key: sessionIdentityStableKey(state(tabId)?.meta) });
    followers.current.get(tabId)?.stop();
    const follower = new TranscriptSessionFollower(tabId, path, false, action => {
      if (followers.current.get(tabId) === follower) dispatch(tabId, action);
    });
    followers.current.set(tabId, follower);
    await follower.start();
    return follower.metrics;
  };

  const sessionPath = (tabId: string) => state(tabId)?.meta?.sessionPath ?? "";

  return {
    start,
    eligible,
    attach(tabId) {
      if (!eligible(tabId)) return;
      void start(tabId, sessionPath(tabId)).catch(() => {});
    },
    attachOnReady(tabId) {
      if (!eligible(tabId)) return;
      void start(tabId, sessionPath(tabId)).catch(error =>
        dispatch(tabId, { type: "transcript_connection", status: "disconnected", error: String(error) }));
    },
  };
}
