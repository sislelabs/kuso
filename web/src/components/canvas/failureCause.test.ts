import { describe, expect, it } from "vitest";
import type { BuildSummary, NodeStatus } from "@/features/services";
import { failureCause } from "./failureCause";

const failing: NodeStatus = { kind: "build_failed", label: "Failing", tone: "error", detail: "Build failed: no image" };
const build = (b: Partial<BuildSummary>): BuildSummary => ({ id: "b", serviceName: "s", status: "failed", ...b });

describe("failureCause", () => {
  it("names a classified build failure", () => {
    expect(failureCause(failing, undefined, build({ failureClass: { kind: "clone_ref_missing", tab: "build", summary: "x" } }))).toBe(
      "Branch not found",
    );
  });
  it("falls back to the step the build died in", () => {
    expect(failureCause(failing, undefined, build({ errorMessage: "container exited with code 1 (clone)" }))).toMatch(/failed$/);
  });
  it("puts the probe result in a failing health check", () => {
    const st: NodeStatus = { kind: "rollout_failing", label: "Rollout failing", tone: "warning" };
    expect(failureCause(st, { rollout: { kind: "healthcheck", probeResult: "404" } })).toBe("Health check 404");
  });
  it("stays quiet for a healthy card", () => {
    expect(failureCause({ kind: "live", label: "Live", tone: "success" }, undefined)).toBeUndefined();
  });
});
