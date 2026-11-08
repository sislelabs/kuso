import { describe, expect, it } from "vitest";
import { ApiError } from "./api-client";
import { classifyError, isRetryable } from "./error-kind";

describe("classifyError", () => {
  it("never offers Retry for a missing or forbidden resource", () => {
    expect(classifyError(new ApiError(404, { error: "project x not found" }, "404"))).toBe("not-found");
    expect(isRetryable(new ApiError(404, null, "404"))).toBe(false);
    expect(isRetryable(new ApiError(403, null, "403"))).toBe(false);
    expect(isRetryable(new ApiError(400, null, "400"))).toBe(false);
  });

  it("offers Retry for server and network failures", () => {
    expect(isRetryable(new ApiError(502, "bad gateway", "502"))).toBe(true);
    expect(isRetryable(new TypeError("Failed to fetch"))).toBe(true);
  });

  it("treats a not_found code on a non-404 status as not-found", () => {
    expect(classifyError(new ApiError(400, { error: "x", code: "not_found" }, "400"))).toBe("not-found");
  });
});
