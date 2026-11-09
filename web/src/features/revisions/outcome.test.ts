import { describe, expect, it } from "vitest";
import { configRolloutOutcome } from "./outcome";
import type { Revision } from "./api";

const rev = (id: string, createdAt: string, snapshot: unknown = { patch: { healthcheck: {} } }): Revision => ({
  id,
  project: "shop",
  kind: "service",
  name: "api",
  snapshot,
  createdAt,
});

describe("configRolloutOutcome", () => {
  const revs = [rev("old", "2026-11-09T09:00:00Z"), rev("new", "2026-11-09T09:10:00Z")];

  it("marks the newest change rolled out once the ReplicaSet finished after it", () => {
    const got = configRolloutOutcome(revs, { status: { state: "running", rolloutCompletedAt: "2026-11-09T09:11:00Z" } });
    expect(got).toEqual({ id: "new", outcome: { label: "Rolled out", tone: "ok", at: "2026-11-09T09:11:00Z" } });
  });

  it("reports a failing rollout from the server state", () => {
    expect(configRolloutOutcome(revs, { status: { state: "rollout_failing" } })?.outcome.label).toBe("Rollout failing");
  });

  it("claims nothing when the change rolled no pods", () => {
    expect(configRolloutOutcome(revs, { status: { state: "running", rolloutCompletedAt: "2026-11-09T08:00:00Z" } })).toBeNull();
  });

  it("defers to a later image promotion", () => {
    const env = {
      metadata: { annotations: { "kuso.sislelabs.com/promoted-at": "2026-11-09T09:20:00Z" } },
      status: { state: "running", rolloutCompletedAt: "2026-11-09T09:21:00Z" },
    };
    expect(configRolloutOutcome(revs, env)).toBeNull();
  });
});
