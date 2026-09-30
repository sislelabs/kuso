import { describe, expect, it } from "vitest";
import type { BuildSummary } from "./api";
import {
  healthProblem,
  matchPodSize,
  memoryPressure,
  needsRestart,
  parseMemoryQuantity,
  pickRollbackTarget,
} from "./overlayState";

function b(id: string, status: string, at: string, extra: Partial<BuildSummary> = {}): BuildSummary {
  return { id, serviceName: "web", status, finishedAt: at, imageTag: id, ...extra };
}

describe("pickRollbackTarget", () => {
  const builds = [
    b("b5", "succeeded", "2026-09-05T00:00:00Z", { branch: "main" }),
    b("b4", "failed", "2026-09-04T00:00:00Z", { branch: "main" }),
    b("b3", "succeeded", "2026-09-03T00:00:00Z", { branch: "staging" }),
    b("b2", "succeeded", "2026-09-02T00:00:00Z", { branch: "main" }),
    b("b1", "succeeded", "2026-09-01T00:00:00Z", { branch: "main" }),
  ];

  it("picks the newest succeeded build on the branch older than live", () => {
    expect(pickRollbackTarget(builds, { imageTag: "b5", branch: "main" })?.id).toBe("b2");
  });

  it("skips builds from another branch", () => {
    expect(pickRollbackTarget(builds, { imageTag: "b3", branch: "staging" })).toBeUndefined();
  });

  it("never targets a build newer than live", () => {
    expect(pickRollbackTarget(builds, { imageTag: "b2", branch: "main" })?.id).toBe("b1");
  });

  it("steps one back from the newest when live is unknown", () => {
    expect(pickRollbackTarget(builds, { branch: "main" })?.id).toBe("b2");
  });

  it("returns undefined when only the live build succeeded", () => {
    expect(pickRollbackTarget([builds[0]], { imageTag: "b5", branch: "main" })).toBeUndefined();
  });

  it("matches live by commit when the image tag is missing", () => {
    const withSha = builds.map((x) => ({ ...x, commitSha: x.id + "0000000" }));
    expect(pickRollbackTarget(withSha, { commit: "b20000", branch: "main" })?.id).toBe("b1");
  });
});

describe("needsRestart", () => {
  const stale = { podsStale: ["envVars"], rolloutPending: false, specPending: [] };
  it("true for stale pods on a healthy env", () => {
    expect(needsRestart(stale, "running")).toBe(true);
    expect(needsRestart(stale, undefined)).toBe(true);
  });
  it("false while a rollout is in flight", () => {
    expect(needsRestart({ ...stale, rolloutPending: true }, "running")).toBe(false);
  });
  it("false when the rollout is stuck (crashloop or image pull)", () => {
    expect(needsRestart(stale, "crashlooping")).toBe(false);
    expect(needsRestart(stale, "deploying")).toBe(false);
  });
  it("false when nothing is stale", () => {
    expect(needsRestart({ podsStale: [] }, "running")).toBe(false);
  });
});

describe("healthProblem", () => {
  it("classifies runtime vs deploy failures", () => {
    expect(healthProblem("crashlooping", "crashed")).toBe("runtime");
    expect(healthProblem("degraded", "degraded")).toBe("runtime");
    expect(healthProblem("build_failed", "failed")).toBe("deploy");
    expect(healthProblem("running", "active")).toBeNull();
    expect(healthProblem(undefined, "failed")).toBe("runtime");
  });
});

describe("memory", () => {
  it("parses k8s quantities", () => {
    expect(parseMemoryQuantity("512Mi")).toBe(512 * 1024 * 1024);
    expect(parseMemoryQuantity("1G")).toBe(1e9);
    expect(parseMemoryQuantity("1024")).toBe(1024);
    expect(parseMemoryQuantity("")).toBeUndefined();
    expect(parseMemoryQuantity("lots")).toBeUndefined();
  });
  it("reports the worst pod against the limit", () => {
    expect(memoryPressure([100, 90], 100)).toBe(1);
    expect(memoryPressure([50], undefined)).toBeUndefined();
  });
});

describe("matchPodSize", () => {
  const presets = [
    { Name: "small", CPURequest: "100m", CPULimit: "500m", MemoryRequest: "128Mi", MemoryLimit: "512Mi" },
  ];
  it("matches a preset, blank, or custom", () => {
    expect(matchPodSize(presets, { cpuRequest: "100m", cpuLimit: "500m", memRequest: "128Mi", memLimit: "512Mi" })).toBe("small");
    expect(matchPodSize(presets, { cpuRequest: "", cpuLimit: "", memRequest: "", memLimit: "" })).toBe("");
    expect(matchPodSize(presets, { cpuRequest: "200m", cpuLimit: "", memRequest: "", memLimit: "" })).toBeNull();
  });
});
