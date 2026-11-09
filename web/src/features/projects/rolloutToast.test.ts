import { describe, expect, it } from "vitest";
import type { KusoEnvironment } from "@/types/projects";
import { rolloutOutcome } from "./rolloutToast";

function env(name: string, status: Record<string, unknown>): KusoEnvironment {
  return { metadata: { name }, spec: { service: "shop-api" }, status } as unknown as KusoEnvironment;
}

const T0 = "2026-11-09T09:00:00Z";
const T1 = "2026-11-09T10:00:00Z";

describe("rolloutOutcome", () => {
  const base = [{ name: "shop-api-production", completedAt: T0, state: "running" }];
  const opts = { sawRolling: false, elapsedMs: 5_000 };

  it("stays pending while the old completion stamp is still there", () => {
    expect(rolloutOutcome(base, [env("shop-api-production", { state: "running", rolloutCompletedAt: T0 })], opts).kind).toBe(
      "pending",
    );
  });
  it("stays pending mid-rollout and remembers it saw one", () => {
    const out = rolloutOutcome(base, [env("shop-api-production", { state: "deploying" })], opts);
    expect(out).toEqual({ kind: "pending", sawRolling: true });
  });
  it("is done when the completion stamp moves and the env is running", () => {
    expect(rolloutOutcome(base, [env("shop-api-production", { state: "running", rolloutCompletedAt: T1 })], opts).kind).toBe(
      "done",
    );
  });
  it("reports a new failure with the server's cause", () => {
    const out = rolloutOutcome(base, [env("shop-api-production", { state: "rollout_failing", stateDetail: "health check 404" })], opts);
    expect(out).toEqual({ kind: "failed", detail: "health check 404" });
  });
  it("doesn't call a fix failed before its rollout started", () => {
    const failing = [{ name: "shop-api-production", completedAt: T0, state: "rollout_failing" }];
    const still = [env("shop-api-production", { state: "rollout_failing" })];
    expect(rolloutOutcome(failing, still, opts).kind).toBe("pending");
    expect(rolloutOutcome(failing, still, { sawRolling: true, elapsedMs: 5_000 }).kind).toBe("failed");
    expect(rolloutOutcome(failing, still, { sawRolling: false, elapsedMs: 200_000 }).kind).toBe("failed");
  });
  it("skips sleeping envs and waits for every other target", () => {
    const two = [...base, { name: "shop-api-staging", completedAt: T0, state: "running" }];
    const envs = [
      env("shop-api-production", { state: "running", rolloutCompletedAt: T1 }),
      env("shop-api-staging", { state: "running", rolloutCompletedAt: T0 }),
    ];
    expect(rolloutOutcome(two, envs, opts).kind).toBe("pending");
    envs[1] = env("shop-api-staging", { state: "sleeping" });
    expect(rolloutOutcome(two, envs, opts).kind).toBe("done");
  });
});
