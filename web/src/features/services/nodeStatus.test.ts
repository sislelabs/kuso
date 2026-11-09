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
      ["release_failed", "degraded", "Degraded"],
      ["crashlooping", "crashing", "Failing"],
      ["deploying", "deploying", "Deploying"],
      ["building", "building", "Building"],
      ["build_failed", "build_failed", "Failing"],
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

  it("keeps the specific cause in the detail, not the label", () => {
    const s = deriveServiceStatus({ env: env({ state: "crashlooping", stateDetail: "OOMKilled" }) });
    expect([s.label, s.detail]).toEqual(["Failing", "Crashing: OOMKilled"]);
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
    expect([s.kind, s.label, s.detail]).toEqual(["crashing", "Failing", "boom"]);
  });

  it("deployed is live, otherwise provisioning", () => {
    expect(deriveAddonStatus(addon([{ type: "Deployed", status: "True" }])).kind).toBe("live");
    expect(deriveAddonStatus(addon([{ type: "Deployed", status: "False" }])).kind).toBe("provisioning");
    expect(deriveAddonStatus(addon([{ type: "ReleaseFailed", status: "False" }, { type: "Deployed", status: "True" }])).kind).toBe(
      "live",
    );
  });
});

describe("deriveAddonStatus with the server rollup", () => {
  const withState = (status: Record<string, unknown>): KusoAddon => ({
    metadata: { name: "p-db" },
    spec: { project: "p", kind: "postgres" },
    status: { conditions: [{ type: "Deployed", status: "True" }], ...status },
  });

  it("a Deployed release whose pod can't schedule is stuck, not live", () => {
    const s = deriveAddonStatus(
      withState({
        state: "stuck",
        stateDetail: "The node is low on disk, so the pod can't be scheduled",
        pods: { ready: 0, total: 1 },
      }),
    );
    expect([s.kind, s.label, s.tone, s.detail]).toEqual([
      "stuck",
      "Stuck",
      "error",
      "The node is low on disk, so the pod can't be scheduled",
    ]);
  });

  it("server state beats the helm conditions", () => {
    expect(deriveAddonStatus(withState({ state: "degraded", stateDetail: "1/2 pods ready" })).kind).toBe("degraded");
    expect(deriveAddonStatus(withState({ state: "live" })).kind).toBe("live");
  });
});

describe("deriveServiceStatus stuck", () => {
  it("maps the server's stuck state with its reason", () => {
    const env = {
      metadata: { name: "p-api-production" },
      spec: { service: "p-api" },
      status: { state: "stuck", stateDetail: "The image tag doesn't exist in the registry" },
    } as unknown as KusoEnvironment;
    const s = deriveServiceStatus({ env });
    expect([s.kind, s.label, s.tone, s.detail]).toEqual(["stuck", "Stuck", "error", "The image tag doesn't exist in the registry"]);
  });
});

describe("deriveServiceStatus rollout", () => {
  it("reads a failing rollout with the previous version serving as degraded, not down", () => {
    const s = deriveServiceStatus({
      env: env({ state: "rollout_failing", stateDetail: "the new version fails its health check (GET /health → 404); the previous version is still serving" }),
    });
    expect([s.kind, s.label, s.tone]).toEqual(["rollout_failing", "Rollout failing", "warning"]);
    expect(s.detail).toContain("→ 404");
  });

  it("names an image pull failure instead of a bare Stuck", () => {
    const s = deriveServiceStatus({
      env: env({ state: "stuck", stateDetail: "The image can't be pulled", stuck: { reason: "image_pull", summary: "The image can't be pulled" } }),
    });
    expect([s.kind, s.label, s.tone]).toEqual(["stuck", "Image pull failed", "error"]);
  });

  it("doesn't call a never-served promotion a deploy", () => {
    expect(lastDeployAt(env({ state: "stuck" }, { "kuso.sislelabs.com/promoted-at": "2026-11-09T10:00:00Z" }))).toBeUndefined();
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
