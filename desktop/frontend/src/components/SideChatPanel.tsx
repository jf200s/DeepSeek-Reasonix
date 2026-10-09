// SideChatPanel renders one companion session inside the dock: the transcript
// of the child tab the host opened, plus the same Composer the main session
// uses. The host builds that child session with a read-only tool registry, so
// the panel never needs a read-only variant of its own — it displays and
// submits, and the session's own tool set is what makes it read-only.
import { memo, useSyncExternalStore } from "react";
import { Composer } from "./Composer";
import { Transcript } from "./Transcript";
import { app } from "../lib/bridge";
import { getTranscriptStore } from "../lib/transcriptStore";
import type { CollaborationMode, ToolApprovalMode } from "../lib/types";
import type { State } from "../lib/useController";

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
  void parentTabId;
  const state = useSideChatState(tabId);
  const items = state?.items ?? [];
  // The panel owns this command rather than the dock region: the region is a
  // pure presentation layer and must not pull the host bridge into its imports.
  const submit = onSubmit ?? ((target: string, display: string, input: string) => {
    void app.SubmitDisplayToTab(target, display, input);
  });

  return (
    <div className="side-chat-panel" data-side-chat-tab-id={tabId}>
      <Transcript
        items={items}
        tabId={tabId}
        geometrySessionKey={`${tabId}:side-chat`}
        running={state?.running ?? false}
        onPrompt={() => {}}
      />
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
        onSend={(displayText, submitText) => {
          const input = submitText ?? displayText;
          if (!input.trim()) return;
          submit(tabId, displayText, input);
        }}
        onCancel={async () => ({ discardedItemIds: [] })}
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
