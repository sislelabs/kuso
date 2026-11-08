import { describe, expect, it } from "vitest";
import type { KusoAddon, KusoEnvironment } from "@/types/projects";
import type { BuildSummary } from "./api";
import { deriveAddonStatus, deriveCronStatus, deriveServiceStatus, lastDeployAt } from "./nodeStatus";

function env(status: NonNullable<KusoEnvironment["status"]> = {}, annotations?: Record<string, string>): KusoEnvironment {
  return {
    metadata: { name: "p-web-production", annotations },
    spec: { service: "p-web" } as KusoEnvironment["spec"],
    status,
  };
}

function build(status: string, finishedAt = "2026-11-01T10:00:00Z"): BuildSummary {
  return { id: "b1", serviceName: "web", status, finishedAt, imageTag: "abc" };
}

const kind = (input: Parameters<typeof deriveServiceStatus>[0]) => deriveServiceStatus(input).kind;

describe("deriveServiceStatus", () => {
  it("stopped wins over a live env and a running build", () => {
    expect(kind({ stopped: true, env: env({ state: "running", ready: true }), latestBuild: build("running") })).toBe(
      "stopped",
    );
  });

  it("an in-flight build wins over the server rollup", () => {
    for (const s of ["pending", "queued", "running"]) {
      expect(kind({ env: env({ state: "running" }), latestBuild: build(s) })).toBe("building");
    }
    expect(kind({ env: env({ state: "running" }), latestBuild: build("deploying") })).toBe("deploying");
  });

  it("maps every server state", () => {
    const cases: [string, string, string][] = [
      ["running", "live", "Live"],
      ["degraded", "degraded", "Degraded"],
      ["release_failed", "degraded", "Release failed"],
      ["crashlooping", "crashing", "Crashing"],
      ["deploying", "deploying", "Deploying"],
      ["building", "building", "Building"],
      ["build_failed", "build_failed", "Build failed"],
      ["sleeping", "sleeping", "Sleeping"],
      ["stopped", "stopped", "Stopped"],
      ["no_image", "not_deployed", "Not deployed"],
    ];
    for (const [state, k, label] of cases) {
      const s = deriveServiceStatus({ env: env({ state }), latestBuild: build("succeeded") });
      expect([state, s.kind, s.label]).toEqual([state, k, label]);
    }
  });

  it("server state beats a stale ready flag", () => {
    expect(kind({ env: env({ state: "crashlooping", ready: true }) })).toBe("crashing");
  });

  it("carries the server's stateDetail", () => {
    expect(deriveServiceStatus({ env: env({ state: "degraded", stateDetail: "1/2 ready" }) }).detail).toBe("1/2 ready");
  });

  it("falls back to heuristics when the server sends no state", () => {
    expect(kind({})).toBe("not_deployed");
    expect(kind({ env: env({ ready: true }) })).toBe("live");
    expect(kind({ env: env({ imageTag: "x" }), latestBuild: build("failed") })).toBe("build_failed");
    expect(kind({ env: env({}) })).toBe("not_deployed");
    expect(kind({ env: env({ imageTag: "x", replicas: { ready: 0, desired: 1 } }) })).toBe("crashing");
    expect(kind({ env: env({ imageTag: "x", replicas: { ready: 1, desired: 1 }, phase: "Sleeping" }) })).toBe("sleeping");
    expect(kind({ env: env({ imageTag: "x", replicas: { ready: 1, desired: 1 } }) })).toBe("unknown");
  });

  it("a succeeded build without an image tag is not 'not deployed'", () => {
    expect(kind({ env: env({ replicas: { ready: 0, desired: 1 } }), latestBuild: build("succeeded") })).toBe("crashing");
  });

  it("each kind has exactly one tone", () => {
    expect(deriveServiceStatus({ env: env({ state: "running" }) }).tone).toBe("success");
    expect(deriveServiceStatus({ env: env({ state: "crashlooping" }) }).tone).toBe("error");
    expect(deriveServiceStatus({ env: env({ state: "degraded" }) }).tone).toBe("warning");
    expect(deriveServiceStatus({ latestBuild: build("queued") }).tone).toBe("progress");
    expect(deriveServiceStatus({ stopped: true }).tone).toBe("muted");
  });
});

describe("deriveAddonStatus", () => {
  const addon = (conditions: { type: string; status: string; message?: string }[]): KusoAddon => ({
    metadata: { name: "p-db" },
    spec: { project: "p", kind: "postgres" },
    status: { conditions },
  });

  it("failure wins over a stale Deployed=True", () => {
    const s = deriveAddonStatus(
      addon([
        { type: "Deployed", status: "True" },
        { type: "ReleaseFailed", status: "True", message: "boom" },
      ]),
    );
    expect([s.kind, s.detail]).toEqual(["build_failed", "boom"]);
  });

  it("deployed is live, otherwise provisioning", () => {
    expect(deriveAddonStatus(addon([{ type: "Deployed", status: "True" }])).kind).toBe("live");
    expect(deriveAddonStatus(addon([{ type: "Deployed", status: "False" }])).kind).toBe("provisioning");
    expect(deriveAddonStatus(addon([{ type: "ReleaseFailed", status: "False" }, { type: "Deployed", status: "True" }])).kind).toBe(
      "live",
    );
  });
});

describe("deriveCronStatus", () => {
  it("suspended crons are paused", () => {
    expect(deriveCronStatus({ suspend: true }).kind).toBe("paused");
    expect(deriveCronStatus({}).kind).toBe("live");
  });
});

describe("lastDeployAt", () => {
  it("takes the newest of promotion, lastDeployedAt and a succeeded build", () => {
    const e = env({ lastDeployedAt: "2026-11-01T08:00:00Z" }, { "kuso.sislelabs.com/promoted-at": "2026-11-01T09:00:00Z" });
    expect(lastDeployAt(e)).toBe("2026-11-01T09:00:00Z");
    expect(lastDeployAt(e, build("succeeded", "2026-11-02T00:00:00Z"))).toBe("2026-11-02T00:00:00Z");
  });

  it("ignores a newer failed build", () => {
    const e = env({ lastDeployedAt: "2026-11-01T08:00:00Z" });
    expect(lastDeployAt(e, build("failed", "2026-11-05T00:00:00Z"))).toBe("2026-11-01T08:00:00Z");
  });

  it("is undefined with nothing to go on, and skips unparseable stamps", () => {
    expect(lastDeployAt(undefined)).toBeUndefined();
    expect(lastDeployAt(env({ lastDeployedAt: "garbage" }))).toBeUndefined();
  });
});
