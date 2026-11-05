import { useCallback } from "react";
import type { ActionDispatch } from "react";
import { DeliveryError, HttpError, type AgentPort, type ApprovalVerdict } from "../port/port";
import type { PlanAction } from "../port/session";
import type { SessionEvent } from "../state/session";

// "unconfirmed" is the one answer the card has to render itself: the kernel may
// have taken the decision, so nothing settles until a receipt or a status read
// says which happened.
export type GateOutcome = "unconfirmed" | void;

interface Inputs {
  port: AgentPort;
  dispatch: ActionDispatch<[SessionEvent]>;
  refreshStatus: () => void;
  fail: (e: unknown) => void;
  onRevise: () => void;
}

// A duplicate answer to a decision the kernel already settled is success seen
// from the losing side: another window or an earlier attempt got there first.
// Reporting it as a failure would leave the person retrying a closed gate.
// A normal answer's ask that is gone refuses with 409 surface.expired;
// ask.not_found (404) is raised only by the engage request, which does not
// travel through this helper.
function alreadySettled(e: unknown): boolean {
  if (!(e instanceof HttpError) || e.status !== 409) return false;
  return e.reason?.code === "plan.decision_stale" || e.reason?.code === "surface.expired";
}

/** What a card sitting at a gate can answer with. Each of these settles one
 *  kernel decision and then re-reads the projection rather than patching it,
 *  because the answer that lands is the kernel's, not this one's. */
export function useGateActions({ port, dispatch, refreshStatus, fail, onRevise }: Inputs) {
  // The ack timeout cannot say whether a decision landed, so one unconfirmed
  // send is retried; a refusal the kernel did answer is final and is not sent
  // again. A duplicate answer means it landed already, so neither path reports
  // it as a failure.
  const decide = useCallback(
    async (send: () => Promise<void>, rethrow = false, retryable = true): Promise<GateOutcome> => {
      for (let attempt = 0; ; attempt++) {
        try {
          await send();
          return;
        } catch (e) {
          if (alreadySettled(e)) {
            refreshStatus();
            return;
          }
          if (retryable && e instanceof DeliveryError && attempt === 0) continue;
          // The kernel may have applied the decision without answering it, so
          // the projection is re-read even though this attempt failed.
          fail(e);
          refreshStatus();
          if (rethrow) throw e;
          return e instanceof DeliveryError ? "unconfirmed" : undefined;
        }
      }
    },
    [fail, refreshStatus],
  );

  // Transcript rows are memoised on their item; a callback rebuilt every render
  // would defeat that on the two cards that take one.
  const onApprove = useCallback(
    async (itemId: string, id: string, v: ApprovalVerdict) =>
      decide(async () => {
        await port.approve(id, v);
        dispatch({ kind: "__decided", id: itemId, verdict: v } as never);
        refreshStatus();
      }),
    [port, dispatch, refreshStatus, decide],
  );

  const onFullAccess = useCallback(
    async (itemId: string) =>
      decide(async () => {
        await port.setApprovalMode("yolo");
        dispatch({ kind: "__decided", id: itemId, verdict: "yolo" } as never);
        refreshStatus();
      }),
    [port, dispatch, refreshStatus, decide],
  );

  // The plan gate has three outcomes the kernel keeps apart, and only one of
  // them is "allow". The other two both deny at the gate and differ in where
  // they leave you: revise stays in plan mode so the next thing you type is
  // feedback on the plan, exit leaves it. Leaving plan mode is the frontend's
  // job — the kernel does not clear the flag when a plan is approved, which is
  // why the chat TUI clears it here too. Studio never did, so approving a plan
  // executed it and then planned the next turn all over again.
  const onPlan = useCallback(
    async (itemId: string, id: string, action: PlanAction) =>
      decide(async () => {
        // The three outcomes are three kernel transitions, so they go back whole
        // rather than as an allow/deny pair. The kernel moves the lifecycle
        // itself — setting plan mode from here would race its own transition —
        // and a stale decision is ordinary concurrency, which decide() treats as
        // already settled rather than binding this answer to whatever is open.
        await port.planDecision(id, action);
        dispatch({ kind: "__decided", id: itemId, verdict: action } as never);
        refreshStatus();
        // Revising is done by talking, so put the cursor where the talking happens.
        if (action === "revise") onRevise();
      }),
    [port, dispatch, refreshStatus, onRevise, decide],
  );

  // Taking it back is only cheap while the conversation that caused it is still
  // on screen; the card stays, marked, so the record of what happened survives.
  const onForget = useCallback(
    (itemId: string, name: string) => {
      port.forgetMemory(name).then(() => dispatch({ kind: "__forgot", id: itemId } as never)).catch(fail);
    },
    [port, fail],
  );

  // An extension action reports back in its own words. Surfacing the result as
  // a notice keeps it in the transcript where the card that offered it sits.
  const onExtInvoke = useCallback(
    (name: string) => {
      port
        .invokeExtensionAction(name)
        .then((message) => {
          if (message.trim()) dispatch({ kind: "notice", level: "info", text: message });
        })
        .catch(fail);
    },
    [port, fail],
  );

  const onExtSubmit = useCallback(
    async (pluginId: string, surfaceId: string, values: Record<string, unknown>) => {
      // Not a decision gate: the plugin's own action may not be idempotent, so
      // a send that timed out is reported rather than repeated.
      await decide(() => port.submitExtensionForm(pluginId, surfaceId, values), true, false);
    },
    [port, decide],
  );

  const onAnswer = useCallback(
    async (itemId: string, id: string, answers: { questionId: string; selected: string[]; value?: Record<string, unknown> }[]) =>
      decide(async () => {
        await port.answer(id, answers);
        dispatch({ kind: "__decided", id: itemId, answers: answers.map((a) => a.selected), values: answers.find(a => a.value !== undefined)?.value } as never);
      }, answers.some(a => a.value !== undefined)),
    [port, dispatch, decide],
  );
  return { onApprove, onFullAccess, onPlan, onForget, onExtInvoke, onExtSubmit, onAnswer };
}
