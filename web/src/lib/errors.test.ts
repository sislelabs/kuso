import { beforeEach, describe, expect, it, vi } from "vitest";

const sonner = vi.hoisted(() => ({ error: vi.fn() }));
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), sonner) }));

import { ApiError } from "./api-client";
import { describeError, isHumanMessage, toastError } from "./errors";

const FB = "Couldn't save the rule";
const err = (status: number, body: unknown) => new ApiError(status, body, `${status} Status`);

describe("describeError", () => {
  it("maps a fetch TypeError to the network sentence", () => {
    expect(describeError(new TypeError("Failed to fetch"), FB)).toBe(
      "Couldn't reach the kuso server. Check your connection and try again.",
    );
  });

  it("maps an abort to a cancelled sentence", () => {
    const e = new Error("aborted");
    e.name = "AbortError";
    expect(describeError(e, FB)).toMatch(/took too long/);
  });

  it("401 always says the session expired, whatever the body", () => {
    expect(describeError(err(401, { error: "unauthorized" }), FB)).toBe(
      "Your session has expired. Sign in again to continue.",
    );
    expect(describeError(err(401, { error: "token signature invalid" }), FB)).toMatch(/session has expired/);
  });

  it("403 generic and with a human reason", () => {
    expect(describeError(err(403, { error: "forbidden" }), FB)).toMatch(/^You don't have permission to do that\. Ask an admin/);
    expect(describeError(err(403, { error: "missing permission secrets:read" }), FB)).toBe(
      "You don't have permission to do that: missing permission secrets:read.",
    );
  });

  it("404 bare vs specific", () => {
    expect(describeError(err(404, { error: "not found" }), FB)).toMatch(/no longer exists/);
    expect(describeError(err(404, { error: "service demo/api not found" }), FB)).toBe("Service demo/api not found.");
  });

  it("409 keeps the human conflict message", () => {
    expect(describeError(err(409, "addon demo/db already exists"), FB)).toBe("Addon demo/db already exists.");
    expect(describeError(err(409, { error: "conflict" }), FB)).toMatch(/changed this at the same time/);
  });

  it("413 / 422 / 400 / 429", () => {
    expect(describeError(err(413, ""), FB)).toMatch(/too large/);
    expect(describeError(err(422, { error: "unprocessable entity" }), FB)).toMatch(/weren't accepted/);
    expect(describeError(err(422, { error: "cron expression must have 5 fields" }), FB)).toBe(
      "Cron expression must have 5 fields.",
    );
    expect(describeError(err(400, { error: "bad request: invalid character 'x' looking for value" }), FB)).toMatch(
      /weren't accepted/,
    );
    expect(describeError(err(429, ""), FB)).toMatch(/Too many requests/);
  });

  it("5xx: generic sentence; 502-504 say restarting; raw internals hidden", () => {
    expect(describeError(err(503, "<html>bad gateway</html>"), FB)).toMatch(/may be restarting/);
    expect(describeError(err(500, { error: "kube: patch: context deadline exceeded" }), FB)).toBe(
      "The kuso server hit an unexpected error. Try again; if it keeps happening, check the server logs.",
    );
    expect(describeError(err(500, { error: "registry is full" }), FB)).toMatch(/unexpected error.*\(Registry is full\)$/);
  });

  it("falls back for unknown shapes", () => {
    expect(describeError(undefined, FB)).toBe(FB);
    expect(describeError({ weird: true }, FB)).toBe(FB);
    expect(describeError(new Error(""), FB)).toBe(FB);
    expect(describeError("plain words", FB)).toBe("Plain words.");
  });
});

describe("isHumanMessage", () => {
  it("rejects status-text fallbacks and plumbing", () => {
    expect(isHumanMessage("500 Internal Server Error")).toBe(false);
    expect(isHumanMessage("dial tcp 10.0.0.1:443: connection refused")).toBe(false);
    expect(isHumanMessage("Internal")).toBe(false);
    expect(isHumanMessage("x".repeat(400))).toBe(false);
    expect(isHumanMessage("domain already used by tickero/web")).toBe(true);
  });
});

describe("toastError", () => {
  beforeEach(() => sonner.error.mockClear());

  it("headline is the fallback, detail is the description", () => {
    toastError(new TypeError("Failed to fetch"), FB);
    expect(sonner.error).toHaveBeenCalledWith(
      FB,
      expect.objectContaining({ description: expect.stringMatching(/Couldn't reach/) }),
    );
  });

  it("offers Retry only when the failure is retryable", () => {
    const retry = vi.fn();
    toastError(err(503, ""), FB, { retry });
    const opts503 = sonner.error.mock.calls[0][1] as { action?: { label: string; onClick: () => void } };
    expect(opts503.action?.label).toBe("Retry");
    opts503.action?.onClick();
    expect(retry).toHaveBeenCalled();

    toastError(err(403, { error: "forbidden" }), FB, { retry });
    const opts403 = sonner.error.mock.calls[1][1] as { action?: unknown };
    expect(opts403.action).toBeUndefined();
  });

  it("omits a description that would repeat the headline", () => {
    toastError(undefined, FB);
    const opts = sonner.error.mock.calls[0][1] as { description?: string };
    expect(opts.description).toBeUndefined();
  });
});
