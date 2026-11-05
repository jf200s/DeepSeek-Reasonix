// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import "./testkit";
import { MockPort } from "../port/mock";
import { HttpError } from "../port/port";
import type { WireEvent } from "../port/wire";
import { Pane, type PaneReport } from "./Pane";

afterEach(() => { cleanup(); vi.useRealTimers(); });

// A pane the person is not looking at. Its state of running comes from two
// reads of the kernel — the frames of its stream, and the status snapshot a turn
// boundary asks for — and when the network drops under the second one the pane
// has no other way to hear that the turn ended.
async function mount() {
  const port = new MockPort();
  const snapshot = await port.status();
  const kernel = { running: false, failing: 0, terminal: false, reads: 0 };
  vi.spyOn(port, "status").mockImplementation(async () => {
    kernel.reads++;
    if (kernel.terminal) throw new HttpError(404, "gone", { code: "request.not_found", params: { kind: "runtime", name: "r1" } });
    if (kernel.failing > 0) {
      kernel.failing--;
      throw new TypeError("network down");
    }
    return { ...snapshot, running: kernel.running };
  });
  let deliver: ((event: WireEvent) => void) | undefined;
  vi.spyOn(port, "subscribe").mockImplementation((onEvent) => {
    deliver = onEvent;
    return () => { deliver = undefined; return true; };
  });
  const onReport = vi.fn<(id: string, report: PaneReport) => void>();
  const onGone = vi.fn<(id: string) => void>();
  const view = render(
    <Pane port={port} rt={{ id: "r1", base: "/rt/r1", root: "/workspace", name: "workspace", sessionPath: "/sessions/a.jsonl" }} title="a" active={false}
      visible={false} sideHost={null} side={false} onFocus={() => {}} onReport={onReport} onSessionChanged={() => {}} onGone={onGone} pulse={0} findPulse={0} onSettings={() => {}}
      needsProject={false} onOpenProject={() => {}} onKeepHere={() => {}} theme="light" dockW={320} dockMax={640} onDockW={() => {}} />,
  );
  const send = (kind: WireEvent["kind"]) => act(() => deliver?.({ kind }));
  const lastLive = () => onReport.mock.calls.at(-1)?.[1].live;
  return { kernel, send, onReport, onGone, lastLive, view };
}

it("hears that a turn ended even when the read that confirms it fails", async () => {
  const pane = await mount();
  pane.kernel.running = true;
  await pane.send("turn_started");
  await waitFor(() => expect(pane.lastLive()).toBe(true));
  pane.kernel.running = false;
  pane.kernel.failing = 1;
  await pane.send("turn_done");
  await waitFor(() => expect(pane.lastLive()).toBe(false), { timeout: 4000 });
});

it("keeps asking while the network stays down, and not in a tight loop", async () => {
  const pane = await mount();
  pane.kernel.running = true;
  await pane.send("turn_started");
  await waitFor(() => expect(pane.lastLive()).toBe(true));
  pane.kernel.running = false;
  pane.kernel.failing = 3;
  const before = pane.kernel.reads;
  await pane.send("turn_done");
  await waitFor(() => expect(pane.lastLive()).toBe(false), { timeout: 9000 });
  const asked = pane.kernel.reads - before;
  expect(asked).toBeGreaterThanOrEqual(4);
  expect(asked).toBeLessThan(8);
}, 15000);

it("stops asking when the pane goes away", async () => {
  const pane = await mount();
  pane.kernel.running = true;
  await pane.send("turn_started");
  await waitFor(() => expect(pane.lastLive()).toBe(true));
  pane.kernel.failing = 100;
  await pane.send("turn_done");
  pane.view.unmount();
  const at = pane.kernel.reads;
  await new Promise((resolve) => setTimeout(resolve, 2500));
  expect(pane.kernel.reads).toBe(at);
}, 10000);

it("retires the pane when its runtime is gone, and stops asking", async () => {
  const pane = await mount();
  pane.kernel.running = true;
  await pane.send("turn_started");
  await waitFor(() => expect(pane.lastLive()).toBe(true));
  pane.kernel.running = false;
  pane.kernel.terminal = true;
  await pane.send("turn_done");
  await waitFor(() => expect(pane.onGone).toHaveBeenCalledWith("r1"), { timeout: 4000 });
  const at = pane.kernel.reads;
  await new Promise((resolve) => setTimeout(resolve, 2000));
  expect(pane.kernel.reads).toBe(at);
}, 10000);
