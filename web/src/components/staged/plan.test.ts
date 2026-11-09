import { describe, expect, it } from "vitest";
import type { KusoService } from "@/types/projects";
import { planService } from "./plan";

const svc = { spec: { port: 8080, scale: { min: 1, max: 3, targetCPU: 70 } } } as unknown as KusoService;

describe("planService", () => {
  it("builds one patch and flags a pod restart for a port change", () => {
    const plan = planService(svc, { settings: { fields: { port: "9090" }, base: { port: "8080" } } });
    expect(plan.patch).toEqual({ port: 9090 });
    expect(plan.restarts).toBe(true);
    expect(plan.conflicts).toEqual([]);
  });

  it("says a scale-only change applies without restart", () => {
    const plan = planService(svc, { settings: { fields: { scaleMax: "5" }, base: { scaleMax: "3" } } });
    expect(plan.patchFields).toEqual(["scale"]);
    expect(plan.restarts).toBe(false);
  });

  it("variables always restart", () => {
    const plan = planService(svc, { env: { set: [{ name: "A", value: "1", before: null, sensitive: false }], unset: [] } });
    expect(plan.restarts).toBe(true);
    expect(plan.patch).toBeUndefined();
  });

  it("reports a setting changed elsewhere since it was staged", () => {
    const plan = planService(svc, { settings: { fields: { port: "9090" }, base: { port: "3000" } } });
    expect(plan.conflicts).toEqual(["port"]);
  });
});
