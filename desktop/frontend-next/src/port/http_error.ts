// The status is the part callers branch on: a 409 from /resume means a turn
// owns that session, which is a question to put to the user rather than a
// failure.

export class HttpError extends Error {
  // The kernel's machine-readable refusal, when it sent one. Callers show it
  // through i18n/kernel.say() rather than printing message: the message is
  // English fallback for logs, not what a reader should see.
  readonly reason?: { code?: string; error?: string; params?: Record<string, string | number> };

  // Whether the answer carried anything a reader can use. A dead process or a
  // proxy answers with neither a code nor a body, and then message is only a
  // path and a number — true of the failure, useless to the person seeing it.
  readonly detailed: boolean;

  constructor(
    readonly status: number,
    message: string,
    reason?: { code?: string; error?: string; params?: Record<string, string | number> },
    detailed = true,
  ) {
    super(message);
    this.reason = reason;
    this.detailed = detailed;
  }
}

export type DeliveryFault = "kernel_busy" | "unreachable" | "ui_stalled";

// A 404 naming a runtime says the hub no longer holds that pane, so every
// later read of it fails the same way. A 404 naming anything inside a live
// runtime is an ordinary failure and keeps its own handling.
export function runtimeGone(e: unknown): boolean {
  return (
    e instanceof HttpError &&
    e.status === 404 &&
    e.reason?.code === "request.not_found" &&
    e.reason.params?.kind === "runtime"
  );
}

// A call the kernel never answered within its bounded wait. The request may or
// may not have landed, so the identity is the fault: the kernel was slow, could
// not be reached, or this window's own event loop was too starved to say which.
export class DeliveryError extends Error {
  constructor(readonly fault: DeliveryFault) {
    super("request unconfirmed: " + fault);
    this.name = "DeliveryError";
  }
}
