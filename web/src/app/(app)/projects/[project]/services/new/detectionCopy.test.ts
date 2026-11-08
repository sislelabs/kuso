import { describe, expect, it } from "vitest";
import type { RepoReadFailure } from "@/features/github";
import { detectionCopy } from "./detectionCopy";

const base = { runtime: "nixpacks", port: "", where: "the repo root" };
const failure: RepoReadFailure = { kind: "server", title: "kuso couldn't reach GitHub", detail: "", retryable: true };

describe("detectionCopy", () => {
  it("never claims kuso read the repository after a failure", () => {
    const c = detectionCopy({ ...base, state: "failed", result: null, failure });
    expect(c.title).toBe("Check these settings");
    expect(c.description).not.toMatch(/read the repository/);
    expect(c.description).toContain("kuso couldn't reach GitHub");
    expect([c.runtime, c.port]).toEqual(["default", "default"]);
  });

  it("marks the fallback (repo read, nothing found) as defaults", () => {
    const c = detectionCopy({
      ...base,
      state: "done",
      result: { runtime: "nixpacks", port: 8080, reason: "fallback" },
      failure: null,
      port: "8080",
    });
    expect(c.title).toBe("Check these settings");
    expect([c.runtime, c.port]).toEqual(["default", "default"]);
  });

  it("marks detected values, and drops the marker on a value the user changed", () => {
    const result = { runtime: "dockerfile" as const, port: 3000, reason: "Dockerfile detected" };
    const ok = detectionCopy({ ...base, state: "done", result, failure: null, runtime: "dockerfile", port: "3000" });
    expect(ok.title).toBe("Review and deploy");
    expect(ok.description).toMatch(/read the repository/);
    expect([ok.runtime, ok.port]).toEqual(["detected", "detected"]);
    const edited = detectionCopy({ ...base, state: "done", result, failure: null, runtime: "dockerfile", port: "4000" });
    expect(edited.port).toBeNull();
  });

  it("says when only the port fell back", () => {
    const c = detectionCopy({
      ...base,
      state: "done",
      result: { runtime: "dockerfile", port: 0, reason: "Dockerfile detected" },
      failure: null,
      runtime: "dockerfile",
    });
    expect(c.title).toBe("Review and deploy");
    expect(c.description).toMatch(/port is a default/);
    expect([c.runtime, c.port]).toEqual(["detected", "default"]);
  });
});
