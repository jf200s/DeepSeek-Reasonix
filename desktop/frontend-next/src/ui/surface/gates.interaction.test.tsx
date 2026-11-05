// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, renderHook, act } from "@testing-library/react";
import { useGateActions } from "../gates";
import { DeliveryError, HttpError, type AgentPort } from "../../port/port";
import { SsePort } from "../../port/sse";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
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

// A normal answer whose ask is gone refuses with 409 surface.expired (tested
// above); 404 ask.not_found is raised only by the engage request, which never
// travels through a gate action. A 404 arriving here is unexpected, so the gate
// reports it instead of silently sealing the card.
it("reports a 404 from a plain answer rather than treating it as settled", async () => {
  const h = gate({ answer: vi.fn().mockRejectedValue(new HttpError(404, "gone", { code: "ask.not_found" })) });
  await act(async () => { await h.result.current.onAnswer("row", "ask", [{ questionId: "q", selected: [] }]); });
  expect(h.fail).toHaveBeenCalledTimes(1);
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

it("retries a full-access switch the kernel did not acknowledge, once", async () => {
  const h = gate({ setApprovalMode: vi.fn().mockRejectedValueOnce(new DeliveryError("kernel_busy")).mockResolvedValueOnce(undefined) });
  await act(async () => { await h.result.current.onFullAccess("row"); });
  expect(h.port.setApprovalMode).toHaveBeenCalledTimes(2);
  expect(h.port.setApprovalMode).toHaveBeenCalledWith("yolo");
  expect(h.dispatch).toHaveBeenCalledWith({ kind: "__decided", id: "row", verdict: "yolo" });
  expect(h.fail).not.toHaveBeenCalled();
});

it("reports a full-access switch that stays unconfirmed, without sealing the card", async () => {
  const h = gate({ setApprovalMode: vi.fn().mockRejectedValue(new DeliveryError("unreachable")) });
  let outcome: unknown;
  await act(async () => { outcome = await h.result.current.onFullAccess("row"); });
  expect(outcome).toBe("unconfirmed");
  expect(h.port.setApprovalMode).toHaveBeenCalledTimes(2);
  expect(h.dispatch).not.toHaveBeenCalled();
  expect(h.fail).toHaveBeenCalledTimes(1);
  expect(h.refreshStatus).toHaveBeenCalled();
});

it("does not retry a full-access refusal the kernel actually answered", async () => {
  const h = gate({ setApprovalMode: vi.fn().mockRejectedValue(new HttpError(500, "server", { code: "boom" })) });
  await act(async () => { await h.result.current.onFullAccess("row"); });
  expect(h.port.setApprovalMode).toHaveBeenCalledTimes(1);
  expect(h.fail).toHaveBeenCalledTimes(1);
  expect(h.refreshStatus).toHaveBeenCalled();
});

// The card's port call must carry the same delivery semantics as approve and
// answer: a refused connection becomes the typed DeliveryError, so the gate can
// offer retry and the unconfirmed answer instead of an endless spinner.
it("maps a refused connection on the full-access switch to an unconfirmed outcome", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
  const dispatch = vi.fn();
  const fail = vi.fn();
  const refreshStatus = vi.fn();
  const port = new SsePort("", "r1");
  const { result } = renderHook(() => useGateActions({ port, dispatch, fail, refreshStatus, onRevise: vi.fn() }));
  let outcome: unknown;
  await act(async () => { outcome = await result.current.onFullAccess("row"); });
  expect(outcome).toBe("unconfirmed");
  expect(fail.mock.calls[0][0]).toBeInstanceOf(DeliveryError);
  expect((fail.mock.calls[0][0] as DeliveryError).fault).toBe("unreachable");
  expect(dispatch).not.toHaveBeenCalled();
});

it("gives the full-access switch the same bounded wait as other decisions", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("fetch", (_: string, init: RequestInit) =>
    new Promise((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
    }),
  );
  const dispatch = vi.fn();
  const fail = vi.fn();
  const port = new SsePort("", "r1");
  const { result } = renderHook(() => useGateActions({ port, dispatch, fail, refreshStatus: vi.fn(), onRevise: vi.fn() }));
  let outcome: unknown;
  const settled = result.current.onFullAccess("row").then((value) => { outcome = value; });
  await vi.advanceTimersByTimeAsync(20_000);
  await vi.advanceTimersByTimeAsync(20_000);
  await settled;
  expect(outcome).toBe("unconfirmed");
  expect(fail.mock.calls[0][0]).toBeInstanceOf(DeliveryError);
  expect((fail.mock.calls[0][0] as DeliveryError).fault).toBe("kernel_busy");
  expect(dispatch).not.toHaveBeenCalled();
});
