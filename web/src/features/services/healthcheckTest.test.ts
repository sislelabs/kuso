import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { healthcheckTestPassed, isHealthcheckTestUnsupported } from "./healthcheckTest";

describe("isHealthcheckTestUnsupported", () => {
  it("treats the router fallback as an older server", () => {
    expect(isHealthcheckTestUnsupported(new ApiError(404, { error: "no API route POST /api/x" }, "404 Not Found"))).toBe(true);
    expect(isHealthcheckTestUnsupported(new ApiError(404, undefined, "404 Not Found"))).toBe(true);
  });
  it("keeps a real not-found answer", () => {
    expect(isHealthcheckTestUnsupported(new ApiError(404, { error: "environment not found" }, "404 Not Found"))).toBe(false);
    expect(isHealthcheckTestUnsupported(new ApiError(400, { error: "projects: invalid: bad" }, "400"))).toBe(false);
  });
});

describe("healthcheckTestPassed", () => {
  const base = { latencyMs: 3, pod: "p", url: "u" };
  it("follows kubelet's 200–399 rule", () => {
    expect(healthcheckTestPassed({ ...base, status: 204 })).toBe(true);
    expect(healthcheckTestPassed({ ...base, status: 302 })).toBe(true);
    expect(healthcheckTestPassed({ ...base, status: 404 })).toBe(false);
    expect(healthcheckTestPassed({ ...base, error: "connection refused" })).toBe(false);
  });
});
