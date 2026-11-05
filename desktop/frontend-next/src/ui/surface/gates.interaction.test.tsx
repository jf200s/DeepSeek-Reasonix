// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, renderHook, act } from "@testing-library/react";
import { useGateActions } from "../gates";
import { DeliveryError, HttpError, type AgentPort } from "../../port/port";

afterEach(cleanup);
it("a typed answer seals only after acknowledgment and preserves a refused prompt", async () => {
  const dispatch = vi.fn();
  const fail = vi.fn();
  const port = { answer: vi.fn().mockRejectedValueOnce(new Error("refused")).mockResolvedValueOnce(undefined) } as unknown as AgentPort;
  const { result } = renderHook(() => useGateActions({ port, dispatch, fail, refreshStatus: vi.fn(), onRevise: vi.fn() }));
  const answers = [{ questionId: "surface", selected: [], value: { count: 2, enabled: false } }];
  await act(async () => { await expect(result.current.onAnswer("row-1", "ask-1", answers)).rejects.toThrow("refused"); });
  expect(dispatch).not.toHaveBeenCalled();
  expect(fail).toHaveBeenCalledTimes(1);
  await act(async () => { await result.current.onAnswer("row-1", "ask-1", answers); });
  expect(dispatch).toHaveBeenCalledWith({ kind: "__decided", id: "row-1", answers: [[]], values: { count: 2, enabled: false } });
});

function gate(over: Partial<AgentPort> = {}) {
  const dispatch = vi.fn();
  const fail = vi.fn();
  const refreshStatus = vi.fn();
  const port = { approve: vi.fn(async () => {}), planDecision: vi.fn(async () => {}), answer: vi.fn(async () => {}), setApprovalMode: vi.fn(async () => {}), ...over } as unknown as AgentPort;
  const { result } = renderHook(() => useGateActions({ port, dispatch, fail, refreshStatus, onRevise: vi.fn() }));
  return { result, dispatch, fail, refreshStatus, port };
}

it("retries a decision the kernel did not acknowledge, once", async () => {
  const h = gate({ approve: vi.fn().mockRejectedValueOnce(new DeliveryError("kernel_busy")).mockResolvedValueOnce(undefined) });
  await act(async () => { await h.result.current.onApprove("row", "gate", "once"); });
  expect(h.port.approve).toHaveBeenCalledTimes(2);
  expect(h.dispatch).toHaveBeenCalledWith({ kind: "__decided", id: "row", verdict: "once" });
  expect(h.fail).not.toHaveBeenCalled();
});

it("reports an unconfirmed decision after the retry, without sealing the card", async () => {
  const h = gate({ approve: vi.fn().mockRejectedValue(new DeliveryError("unreachable")) });
  let outcome: unknown;
  await act(async () => { outcome = await h.result.current.onApprove("row", "gate", "once"); });
  expect(outcome).toBe("unconfirmed");
  expect(h.port.approve).toHaveBeenCalledTimes(2);
  expect(h.dispatch).not.toHaveBeenCalled();
  expect(h.fail).toHaveBeenCalledTimes(1);
  expect(h.refreshStatus).toHaveBeenCalled();
});

it("treats a stale plan decision as already settled", async () => {
  const h = gate({ planDecision: vi.fn().mockRejectedValue(new HttpError(409, "stale", { code: "plan.decision_stale" })) });
  await act(async () => { await h.result.current.onPlan("row", "gate", "start"); });
  expect(h.fail).not.toHaveBeenCalled();
  expect(h.dispatch).not.toHaveBeenCalled();
  expect(h.refreshStatus).toHaveBeenCalled();
});

it("treats an expired extension surface as already settled", async () => {
  const h = gate({ answer: vi.fn().mockRejectedValue(new HttpError(409, "expired", { code: "surface.expired" })) });
  await act(async () => { await h.result.current.onAnswer("row", "ask", [{ questionId: "q", selected: [] }]); });
  expect(h.fail).not.toHaveBeenCalled();
  expect(h.dispatch).not.toHaveBeenCalled();
  expect(h.refreshStatus).toHaveBeenCalled();
});

it("treats an ask that no longer exists as already settled", async () => {
  const h = gate({ answer: vi.fn().mockRejectedValue(new HttpError(404, "gone", { code: "ask.not_found" })) });
  await act(async () => { await h.result.current.onAnswer("row", "ask", [{ questionId: "q", selected: [] }]); });
  expect(h.fail).not.toHaveBeenCalled();
  expect(h.dispatch).not.toHaveBeenCalled();
  expect(h.refreshStatus).toHaveBeenCalled();
});

it("does not retry a refusal the kernel actually answered", async () => {
  const h = gate({ approve: vi.fn().mockRejectedValue(new HttpError(500, "server", { code: "boom" })) });
  await act(async () => { await h.result.current.onApprove("row", "gate", "once"); });
  expect(h.port.approve).toHaveBeenCalledTimes(1);
  expect(h.fail).toHaveBeenCalledTimes(1);
  expect(h.refreshStatus).toHaveBeenCalled();
});
