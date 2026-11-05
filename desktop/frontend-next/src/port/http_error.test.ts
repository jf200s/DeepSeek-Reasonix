import { describe, expect, it } from "vitest";
import { HttpError, runtimeGone } from "./http_error";

const gone = () => new HttpError(404, "no runtime named r1", { code: "request.not_found", params: { kind: "runtime", name: "r1" } });

describe("a runtime the hub no longer holds", () => {
  it("is the runtime not-found refusal", () => {
    expect(runtimeGone(gone())).toBe(true);
  });

  it("is not another 404 that happens to carry the same code", () => {
    expect(runtimeGone(new HttpError(404, "no session named s", { code: "request.not_found", params: { kind: "session" } }))).toBe(false);
  });

  it("is not a refusal with a different status", () => {
    expect(runtimeGone(new HttpError(410, "gone", { code: "request.not_found", params: { kind: "runtime" } }))).toBe(false);
    expect(runtimeGone(new HttpError(409, "busy", { code: "request.not_found", params: { kind: "runtime" } }))).toBe(false);
  });

  it("is not an ordinary failure", () => {
    expect(runtimeGone(new TypeError("Failed to fetch"))).toBe(false);
    expect(runtimeGone(undefined)).toBe(false);
  });
});
