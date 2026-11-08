import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { isMissingEndpoint, pickEnvExampleEntries, planEnvExampleImport } from "./envExample";

describe("isMissingEndpoint", () => {
  it("treats a route 404 as an older server, not a missing service", () => {
    expect(isMissingEndpoint(new ApiError(404, { error: "no API route GET /api/x" }, ""))).toBe(true);
    expect(isMissingEndpoint(new ApiError(404, { error: "service api not found" }, ""))).toBe(false);
    expect(isMissingEndpoint(new ApiError(400, { error: "nope" }, ""))).toBe(false);
  });
});

describe("env example import", () => {
  const entries = [
    { key: "DATABASE_URL", value: "postgres://localhost/app" },
    { key: "STRIPE_KEY", value: "" },
    { key: "PORT_NAME", value: "x" },
  ];

  it("marks keys the service already has", () => {
    const rows = planEnvExampleImport(entries, new Set(["DATABASE_URL"]));
    expect(rows.map((r) => r.exists)).toEqual([true, false, false]);
  });

  it("never returns an existing key, even one added after the preview", () => {
    const rows = planEnvExampleImport(entries, new Set(["DATABASE_URL"]));
    const all = new Set(entries.map((e) => e.key));
    expect(pickEnvExampleEntries(rows, all, new Set(["DATABASE_URL", "PORT_NAME"]))).toEqual([
      { name: "STRIPE_KEY", value: "" },
    ]);
  });
});
