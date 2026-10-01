import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { friendlyApiError, projectNameError, serviceSlugError } from "./names";

describe("projectNameError", () => {
  it("rejects names the server reserves, including summary", () => {
    expect(projectNameError("summary")).toMatch(/reserved/);
    expect(projectNameError("new")).toMatch(/reserved/);
    expect(projectNameError("kuso-x")).toMatch(/kuso-/);
  });

  it("caps length at the server's 40, not 63", () => {
    expect(projectNameError("a".repeat(40))).toBeNull();
    expect(projectNameError("a".repeat(41))).toMatch(/40/);
  });
});

describe("serviceSlugError", () => {
  it("keeps <project>-<svc>-production within helm's 53 chars", () => {
    const project = "p".repeat(30);
    // 30 + 1 + 10 + 11 = 52
    expect(serviceSlugError(project, "s".repeat(10))).toBeNull();
    expect(serviceSlugError(project, "s".repeat(11))).toBeNull();
    expect(serviceSlugError(project, "s".repeat(12))).toMatch(/at most 11/);
  });
});

describe("friendlyApiError", () => {
  it("strips the wrapped sentinel prefix", () => {
    const e = new ApiError(400, { error: "projects: invalid: name must be 40 characters or fewer" }, "Bad Request");
    expect(friendlyApiError(e, "Failed")).toBe("Name must be 40 characters or fewer");
  });

  it("explains a bare internal error", () => {
    const e = new ApiError(500, { error: "internal" }, "Internal Server Error");
    expect(friendlyApiError(e, "Failed to add service")).toMatch(/^Failed to add service: .*HTTP 500/);
  });
});
