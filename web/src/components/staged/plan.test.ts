import { describe, expect, it } from "vitest";
import type { KusoService } from "@/types/projects";
import { healthcheckCheck, planService, rolloutImpact } from "./plan";

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

  it("attaches the last health check test to the staged path", () => {
    const settings = { fields: { hcEnabled: true, hcPath: "/healthz" }, base: { hcEnabled: false, hcPath: "" } };
    const test = { path: "/healthz", port: "8080", passed: false, summary: "404 Not Found", at: 1 };
    const failed = { settings, healthcheckTest: test };
    expect(healthcheckCheck(planService(svc, failed), failed)).toEqual({ kind: "failed", summary: "404 Not Found", path: "/healthz" });
    const other = { settings, healthcheckTest: { ...test, path: "/other" } };
    expect(healthcheckCheck(planService(svc, other), other)?.kind).toBe("untested");
    const passed = { settings, healthcheckTest: { ...test, passed: true, summary: "200 OK" } };
    expect(healthcheckCheck(planService(svc, passed), passed)?.kind).toBe("passed");
  });

  it("builds when a source setting changes, never for image services", () => {
    const repoSvc = { spec: { port: 8080, runtime: "dockerfile", repo: { url: "https://github.com/a/b", defaultBranch: "main" } } } as unknown as KusoService;
    const branch = planService(repoSvc, { settings: { fields: { repoBranch: "dev" }, base: { repoBranch: "main" } } });
    expect(branch.patchFields).toContain("repo");
    expect(branch.builds).toBe(true);
    expect(planService(repoSvc, { settings: { fields: { port: "9090" }, base: { port: "8080" } } }).builds).toBe(false);
    const imageSvc = { spec: { port: 80, runtime: "image", image: { repository: "nginx", tag: "1" } } } as unknown as KusoService;
    expect(planService(imageSvc, { settings: { fields: { port: "81" }, base: { port: "80" } } }, { neverDeployed: true }).builds).toBe(false);
  });

  it("builds any change to a service that has never deployed", () => {
    const repoSvc = { spec: { port: 8080, runtime: "dockerfile", repo: { url: "https://github.com/a/b" } } } as unknown as KusoService;
    const env = { env: { set: [{ name: "A", value: "1", before: null, sensitive: false }], unset: [] } };
    expect(planService(repoSvc, env, { neverDeployed: true }).builds).toBe(true);
    expect(planService(repoSvc, env).builds).toBe(false);
  });
});

describe("rolloutImpact", () => {
  const gitSvc = { spec: { port: 8080, repo: { url: "https://github.com/a/b", branch: "dev" } } } as unknown as KusoService;

  it("never says 'No restart' when the apply starts a build", () => {
    const plan = planService(gitSvc, { settings: { fields: { repoBranch: "main" }, base: { repoBranch: "dev" } } });
    expect(plan.builds).toBe(true);
    expect(plan.restarts).toBe(false);
    expect(rolloutImpact(plan)).toEqual({ label: "Builds, then deploys", rolls: true });
  });

  it("says pods roll twice when variables apply now and a build follows", () => {
    expect(rolloutImpact({ builds: true, restarts: true }).label).toBe("Restarts pods now and again after the build");
    // Nothing is running yet, so the only rollout is the first deploy.
    expect(rolloutImpact({ builds: true, restarts: true }, true).label).toBe("Builds, then deploys");
  });

  it("keeps the plain restart / no-restart labels without a build", () => {
    expect(rolloutImpact({ builds: false, restarts: true })).toEqual({ label: "Restarts pods once", rolls: true });
    expect(rolloutImpact({ builds: false, restarts: false })).toEqual({ label: "No restart", rolls: false });
  });
});
