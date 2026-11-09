import { describe, expect, it } from "vitest";
import { defaultPlan, previousConfig } from "./previous";
import type { Revision } from "./api";

const rev = (id: string, minutesAgo: number, snapshot: unknown): Revision => ({
  id,
  project: "shop",
  kind: "service",
  name: "api",
  snapshot,
  createdAt: new Date(Date.UTC(2026, 10, 9, 12, 0) - minutesAgo * 60_000).toISOString(),
});

describe("previousConfig", () => {
  it("replays the newest older revision that set the same fields", () => {
    const plan = previousConfig([
      rev("bad", 1, { patch: { healthcheck: { path: "/nope" } } }),
      rev("env", 2, { op: "service.envVars", envVars: [] }),
      rev("good", 3, { patch: { healthcheck: {} } }),
      rev("older", 9, { patch: { healthcheck: { path: "/health" } } }),
    ]);
    expect(plan?.change.id).toBe("bad");
    expect(plan?.target.id).toBe("good");
  });

  it("never picks a revision that also changed other fields", () => {
    const plan = previousConfig([
      rev("bad", 1, { patch: { healthcheck: { path: "/nope" } } }),
      rev("mixed", 3, { patch: { healthcheck: {}, port: 3000 } }),
    ]);
    expect(plan).toBeNull();
  });

  it("matches whole-list snapshots by op and skips informational rows", () => {
    const plan = previousConfig([
      rev("info", 0, { informational: true, op: "service.rename" }),
      rev("vars2", 1, { op: "service.envVars", envVars: [{ name: "A" }] }),
      rev("vars1", 5, { op: "service.envVars", envVars: [] }),
    ]);
    expect(plan?.change.id).toBe("vars2");
    expect(plan?.target.id).toBe("vars1");
  });
});

describe("defaultPlan", () => {
  it("unsets a first-ever health check", () => {
    const plan = defaultPlan([rev("hc", 1, { patch: { healthcheck: { path: "/healthz-does-not-exist" } } }), rev("p", 9, { patch: { port: 80 } })]);
    expect(plan?.change.id).toBe("hc");
    expect(plan?.patch).toEqual({ healthcheck: { path: "" } });
    expect(plan?.label).toBe("the default TCP port check");
  });

  it("has nothing to reset for a field without a default", () => {
    expect(defaultPlan([rev("img", 1, { patch: { image: { repository: "nginx", tag: "x" } } })])).toBeNull();
    expect(defaultPlan([rev("mix", 1, { patch: { healthcheck: { path: "/h" }, port: 80 } })])).toBeNull();
    expect(defaultPlan([])).toBeNull();
  });
});
