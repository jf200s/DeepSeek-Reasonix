// SideChatPanel renders one companion session inside the dock: the transcript
// of the child tab the host opened, plus the same Composer the main session
// uses. The host builds that child session with a read-only tool registry, so
// the panel never needs a read-only variant of its own — it displays and
// submits, and the session's own tool set is what makes it read-only.
import { lazy, memo, Suspense, useEffect, useSyncExternalStore, useState } from "react";
import { Composer } from "./Composer";
import { Transcript } from "./Transcript";
import "./SideChatPanel.css";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { getTranscriptStore } from "../lib/transcriptStore";
import { subscribeSideChatDraft, takeSideChatDraft, type SideChatDraft } from "../lib/sideChatDraft";
import type { CollaborationMode, ToolApprovalMode } from "../lib/types";
import { initialState, type Item, type State } from "../lib/useController";

// The same decision cards the main area shows. They stay lazy so a companion
// with no pending prompt never loads them.
const AskCard = lazy(() => import("./AskCard").then((module) => ({ default: module.AskCard })));
const ApprovalModal = lazy(() => import("./ApprovalModal").then((module) => ({ default: module.ApprovalModal })));

/** The dock shows one companion per tab; its state lives in the per-tab store. */
function useSideChatState(tabId: string): State | null {
  const store = getTranscriptStore();
  return useSyncExternalStore(
    (listener) => store.subscribeState(tabId, listener),
    () => store.states.get(tabId) ?? null,
    () => null,
  );
}

export type SideChatPanelProps = {
  /** Companion (child) session tab whose transcript this panel shows. */
  tabId: string;
  /** Parent session tab that opened the companion. */
  parentTabId: string;
  /** Working directory shown by the composer. */
  cwd?: string;
  /** Forwards typed input to the companion session. Defaults to the host command. */
  onSubmit?: (tabId: string, display: string, input: string) => void;
};

export const SideChatPanel = memo(function SideChatPanel({
  tabId,
  parentTabId,
  cwd,
  onSubmit,
}: SideChatPanelProps) {
  const t = useT();
  const state = useSideChatState(tabId);
  const items = state?.items ?? [];

  // The owner's title, so a dock holding several companions says which
  // conversation each one belongs to. It is read through the host rather than
  // the dock store: an owner is a main-area tab and never appears among the
  // dock tabs, so the dock store has nothing to look up.
  const [parentLabel, setParentLabel] = useState("");
  useEffect(() => {
    let cancelled = false;
    void Promise.resolve()
      .then(() => app.MetaForTab(parentTabId))
      .then((meta) => {
        if (cancelled) return;
        setParentLabel(meta?.label ?? "");
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [parentTabId]);
  // The host creates the companion child tab, so the main-area tab flow never
  // hydrates it. Without a state carrying that tab's meta, every event tagged
  // for the companion fails the controller's sessionGeneration check and the
  // panel stays on its empty hero while the backend is actually running the
  // turn. Registering the meta here is what lets the shared per-tab reducer
  // accept the companion's events.
  useEffect(() => {
    let cancelled = false;
    // Routed through a promise so a host without the command (older shell, test
    // stub) degrades to "no meta registered" instead of throwing during mount.
    void Promise.resolve()
      .then(() => app.MetaForTab(tabId))
      .then((meta) => {
        if (cancelled || !meta) return;
        const store = getTranscriptStore();
        const current = store.states.get(tabId);
        store.setState(tabId, { ...(current ?? initialState), meta });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [tabId]);

  // The companion already has history when the panel mounts (the session may
  // have been running before the dock tab existed). Nothing else hydrates a
  // child tab, so without this the panel shows its empty hero while the
  // conversation continues on the host — the exact "sent a message, nothing
  // happens" symptom. Live turns still arrive through the shared event stream,
  // so this only seeds the transcript once and never overwrites richer items.
  useEffect(() => {
    let cancelled = false;
    void Promise.resolve()
      .then(() => app.HistoryForTab(tabId))
      .then((messages) => {
        if (cancelled) return;
        if (!Array.isArray(messages)) {
          setHostError(`history: unexpected payload ${typeof messages}`);
          return;
        }
        const store = getTranscriptStore();
        const current = store.states.get(tabId);
        if (current && current.items.length > 0) return;
        const items: Item[] = [];
        messages.forEach((message, index) => {
          const text = typeof message.content === "string" ? message.content : "";
          if (!text.trim() || message.role === "system") return;
          // A companion's history is display-only here, so a missing id falls back
          // to its position instead of failing the projection.
          const id = message.messageId ?? `${tabId}-history-${index}`;
          if (message.role === "user") items.push({ kind: "user", id, text });
          else if (message.role === "assistant") {
            items.push({ kind: "assistant", id, text, reasoning: "", streaming: false });
          }
        });
        store.setState(tabId, { ...(current ?? initialState), items });
      })
      .catch((error) => {
        setHostError(`history: ${error instanceof Error ? error.message : String(error)}`);
      });
    return () => {
      cancelled = true;
    };
  }, [tabId]);

  // A pending ask or approval lives on the host until something replays it, and
  // the companion is never the active main tab, so nothing else does. Without
  // this a companion blocked on a question stays blocked with no way to answer.
  useEffect(() => {
    void Promise.resolve()
      .then(() => app.ReplayPendingPromptsForTab(tabId))
      .catch(() => {});
  }, [tabId]);

  // The panel owns this command rather than the dock region: the region is a
  // pure presentation layer and must not pull the host bridge into its imports.
  //
  // Every host call is caught: a rejected submit used to escape as an
  // unhandledrejection and raise the shell's crash overlay over the whole
  // window, which is why a refused send looked like "nothing happens". The
  // companion reports the refusal in place instead, like the main area does.
  // The transcript's selection action hands its text here instead of submitting
  // it: the companion composer starts from the quoted selection and the user
  // finishes the question. A draft deposited before this panel mounted is taken
  // on mount; one deposited while it is open arrives by subscription.
  const [askDraft, setAskDraft] = useState<SideChatDraft | null>(null);
  useEffect(() => {
    // Subscribe first, then drain: a draft deposited before this panel mounted
    // is still taken, one deposited while it is open arrives by subscription, and
    // a second run of this effect (StrictMode, or a panel that remounted) neither
    // drops the draft already shown nor takes the next one twice.
    const unsubscribe = subscribeSideChatDraft(tabId, setAskDraft);
    setAskDraft((current) => current ?? takeSideChatDraft(tabId));
    return unsubscribe;
  }, [tabId]);

  const [hostError, setHostError] = useState<string | null>(null);
  const runHost = (call: Promise<unknown>) => {
    call.catch((error) => setHostError(error instanceof Error ? error.message : String(error)));
  };
  const submit = onSubmit ?? ((target: string, display: string, input: string) => {
    setHostError(null);
    runHost(app.SubmitDisplayToTab(target, display, input));
  });

  return (
    <div className="side-chat-panel" data-side-chat-tab-id={tabId}>
      {parentLabel ? (
        <div className="side-chat-panel__parent" data-side-chat-parent-id={parentTabId}>
          {t("sideChat.fromParent", { label: parentLabel })}
        </div>
      ) : null}
      {hostError ? (
        <div className="side-chat-panel__error" role="alert">
          {hostError}
        </div>
      ) : null}
      <Transcript
        items={items}
        tabId={tabId}
        geometrySessionKey={`${tabId}:side-chat`}
        running={state?.running ?? false}
        onPrompt={() => {}}
      />
      {state?.ask ? (
        <div className="side-chat-panel__decision">
          <Suspense fallback={null}>
            <AskCard
              ask={state.ask}
              draftScope={`side-chat:${tabId}`}
              onAnswer={(id, answers) => {
                runHost(app.AnswerQuestionForTab(tabId, id, answers));
              }}
              onDismiss={() => {
                const ask = state.ask;
                if (ask) runHost(app.AnswerQuestionForTab(tabId, ask.id, []));
              }}
              onStop={() => {
                runHost(app.CancelTab(tabId));
              }}
            />
          </Suspense>
        </div>
      ) : null}
      {state?.approval ? (
        <div className="side-chat-panel__decision">
          <Suspense fallback={null}>
            <ApprovalModal
              approval={state.approval}
              tabId={tabId}
              cwd={cwd}
              onAnswer={(allow, session, persist) => {
                const approval = state.approval;
                if (approval) runHost(app.ApproveTab(tabId, approval.id, allow, session, persist));
              }}
              onStop={() => {
                runHost(app.CancelTab(tabId));
              }}
            />
          </Suspense>
        </div>
      ) : null}
      <Composer
        running={state?.running ?? false}
        collaborationMode={"normal" as CollaborationMode}
        toolApprovalMode={"ask" as ToolApprovalMode}
        goal=""
        cwd={cwd ?? ""}
        modelLabel=""
        tabId={tabId}
        sessionKey={tabId}
        inboxSessionPath=""
        // The companion is read-only through its tool set, not through a muted
        // composer: the user still has to ask the question.
        readOnly={false}
        // A selection handed over by the transcript action starts the question
        // here; the user sends it once it says what they meant to ask.
        selectedTextRequest={askDraft}
        onSend={(displayText, submitText) => {
          const input = submitText ?? displayText;
          if (!input.trim()) return;
          submit(tabId, displayText, input);
        }}
        onCancel={async () => {
          // The companion's own running turn is cancelled through its tab; the
          // empty outcome keeps the composer's un-sent text (a companion has no
          // durable queue to withdraw from).
          runHost(app.CancelTab(tabId));
          return { discardedItemIds: [] };
        }}
        // A companion is read-only and ephemeral: it has no task mode, approval
        // mode, goal or queue to change, and the host implements no model or
        // effort switch for it. hostControls={false} keeps those controls out of
        // the composer, so the callbacks below stay the inert values the props
        // require instead of buttons that look live and do nothing.
        hostControls={false}
        onCycleMode={() => {}}
        onSetMode={() => {}}
        onSetCollaborationMode={() => {}}
        onSetToolApprovalMode={() => {}}
        onClearGoal={() => {}}
        onEditGoal={() => {}}
        onPauseGoal={() => {}}
        onResumeGoal={() => {}}
        onSwitchModel={() => false}
        onSetEffort={() => {}}
        ready
      />
    </div>
  );
});
