import { describe, expect, it } from "vitest";
import type { KusoEnvironment, KusoProject, KusoService } from "@/types/projects";
import {
  lastDeployedAt,
  projectLastDeployedAt,
  projectPrimaryDomain,
  projectStatsLabel,
  serviceIssues,
  projectHealthLine,
  PROMOTED_AT_ANNOTATION,
} from "./facts";

const project: KusoProject = { metadata: { name: "shop" }, spec: {} } as KusoProject;

function svc(short: string, spec: Partial<KusoService["spec"]> = {}, labels: Record<string, string> = {}): KusoService {
  return {
    metadata: { name: `shop-${short}`, labels },
    spec: { project: "shop", port: 8080, ...spec },
  } as KusoService;
}

function env(
  short: string,
  opts: {
    group?: string;
    promoted?: string;
    deployed?: string;
    state?: NonNullable<KusoEnvironment["status"]>["state"];
    host?: string;
    additionalHosts?: string[];
    tls?: boolean;
  } = {},
): KusoEnvironment {
  const group = opts.group ?? "production";
  return {
    metadata: {
      name: `shop-${short}-${group}`,
      labels: { "kuso.sislelabs.com/env": group },
      annotations: opts.promoted ? { [PROMOTED_AT_ANNOTATION]: opts.promoted } : {},
    },
    spec: {
      project: "shop",
      service: `shop-${short}`,
      kind: "production",
      host: opts.host ?? `${short}.shop.kuso.app`,
      additionalHosts: opts.additionalHosts,
      tlsEnabled: opts.tls,
    },
    status: { lastDeployedAt: opts.deployed, state: opts.state },
  };
}

describe("lastDeployedAt", () => {
  it("is undefined without an env or any stamp", () => {
    expect(lastDeployedAt(undefined)).toBeUndefined();
    expect(lastDeployedAt(env("web"))).toBeUndefined();
  });

  it("takes the newer of the promotion stamp and status.lastDeployedAt", () => {
    expect(lastDeployedAt(env("web", { promoted: "2026-11-02T10:00:00Z", deployed: "2026-11-01T10:00:00Z" }))).toBe(
      "2026-11-02T10:00:00Z",
    );
    expect(lastDeployedAt(env("web", { promoted: "2026-11-01T10:00:00Z", deployed: "2026-11-03T10:00:00Z" }))).toBe(
      "2026-11-03T10:00:00Z",
    );
  });

  it("counts a config-only rollout's completion as the last deploy", () => {
    const e = env("web", { promoted: "2026-11-01T10:00:00Z" });
    e.status = { ...e.status, rolloutCompletedAt: "2026-11-05T09:30:00Z" };
    expect(lastDeployedAt(e)).toBe("2026-11-05T09:30:00Z");
  });

  it("doesn't call a promotion that never rolled out a deploy", () => {
    const e = env("web", { promoted: "2026-11-05T10:00:00Z", state: "stuck" });
    expect(lastDeployedAt(e)).toBeUndefined();
    e.status = { ...e.status, state: "rollout_failing", rollout: { since: "2026-11-05T10:00:05Z" } };
    expect(lastDeployedAt(e)).toBeUndefined();
    e.status = { ...e.status, rollout: { since: "2026-11-05T10:00:05Z", previousSince: "2026-11-02T08:00:00Z" } };
    expect(lastDeployedAt(e)).toBe("2026-11-02T08:00:00Z");
  });

  it("keeps the promotion that's still serving under a failing config rollout", () => {
    const e = env("web", { promoted: "2026-11-01T10:00:00Z", state: "rollout_failing" });
    e.status = { ...e.status, rollout: { since: "2026-11-09T09:03:24Z" } };
    expect(lastDeployedAt(e)).toBe("2026-11-01T10:00:00Z");
  });

  it("ignores unparseable stamps", () => {
    expect(lastDeployedAt(env("web", { promoted: "garbage", deployed: "2026-11-01T10:00:00Z" }))).toBe(
      "2026-11-01T10:00:00Z",
    );
  });
});

describe("projectLastDeployedAt", () => {
  it("is the newest production deploy and ignores other env-groups", () => {
    const envs = [
      env("web", { deployed: "2026-11-01T00:00:00Z" }),
      env("api", { promoted: "2026-11-04T00:00:00Z" }),
      env("api", { group: "staging", deployed: "2026-11-07T00:00:00Z" }),
    ];
    expect(projectLastDeployedAt(envs)).toBe("2026-11-04T00:00:00Z");
    expect(projectLastDeployedAt([])).toBeUndefined();
  });
});

describe("projectPrimaryDomain", () => {
  it("prefers a service with a custom domain over the alphabetically first one", () => {
    const services = [svc("api"), svc("web")];
    const envs = [env("api", { state: "running" }), env("web", { additionalHosts: ["shop.com"] })];
    expect(projectPrimaryDomain(project, services, envs)).toMatchObject({ host: "shop.com", href: "https://shop.com" });
  });

  it("falls back to the auto host, live service first, then by name", () => {
    const services = [svc("b"), svc("a"), svc("c")];
    expect(projectPrimaryDomain(project, services, [env("a"), env("b"), env("c", { state: "running" })])).toEqual({
      host: "c.shop.kuso.app",
      href: "https://c.shop.kuso.app",
    });
    expect(projectPrimaryDomain(project, services, [env("a"), env("b"), env("c")])?.host).toBe("a.shop.kuso.app");
  });

  it("skips internal, port-less and env-group clone services", () => {
    const services = [
      svc("a", { internal: true }),
      svc("b", { port: 0 }),
      svc("c", {}, { "kuso.sislelabs.com/env": "staging" }),
      svc("d"),
    ];
    const envs = [env("a"), env("b"), env("c"), env("d")];
    expect(projectPrimaryDomain(project, services, envs)?.host).toBe("d.shop.kuso.app");
  });

  it("uses the env's TLS flag for the scheme, like the canvas", () => {
    expect(projectPrimaryDomain(project, [svc("web")], [env("web", { tls: false })])?.href).toBe(
      "http://web.shop.kuso.app",
    );
  });

  it("reads a service-level custom domain when the env has no host", () => {
    const e = env("web");
    e.spec.host = undefined;
    expect(projectPrimaryDomain(project, [svc("web", { domains: [{ host: "shop.io", tls: true }] })], [e])).toMatchObject({
      host: "shop.io",
      href: "https://shop.io",
    });
  });

  it("is undefined when nothing is public", () => {
    expect(projectPrimaryDomain(project, [svc("worker", { port: undefined })], [env("worker")])).toBeUndefined();
    expect(projectPrimaryDomain(project, [], [])).toBeUndefined();
  });
});

// Real projects from the live instance (services, ports, internal flags
// and production hosts as `kuso api` reports them).
function realCase(
  name: string,
  rows: { short: string; host: string; add?: string[]; internal?: boolean; group?: string }[],
  baseDomain?: string,
) {
  const p = { metadata: { name }, spec: { baseDomain } } as KusoProject;
  const services = rows.map(
    (r) => ({ metadata: { name: `${name}-${r.short}` }, spec: { project: name, port: 3000, internal: r.internal } }) as KusoService,
  );
  const envs = rows.map(
    (r) =>
      ({
        metadata: { name: `${name}-${r.short}-production`, labels: { "kuso.sislelabs.com/env": r.group ?? "production" } },
        spec: { project: name, service: `${name}-${r.short}`, kind: "production", host: r.host, additionalHosts: r.add },
        status: { state: "running" },
      }) as KusoEnvironment,
  );
  return projectPrimaryDomain(p, services, envs)?.host;
}

describe("projectPrimaryDomain on real projects", () => {
  it("tickero: the apex tickero.bg, not api.tickero.bg", () => {
    expect(
      realCase("tickero", [
        { short: "worker", host: "worker.tickero.sislelabs.com" },
        { short: "api", host: "api.tickero.sislelabs.com", add: ["api.tickero.bg"] },
        { short: "frontend", host: "frontend.tickero.sislelabs.com", add: ["tickero.bg"] },
        { short: "backoffice", host: "backoffice.tickero.sislelabs.com", add: ["app.tickero.bg"] },
      ]),
    ).toBe("tickero.bg");
  });

  it("puncho: the apex puncho.app over app.puncho.app; staging clones and internal services don't count", () => {
    expect(
      realCase("puncho", [
        { short: "web-staging", host: "web-staging.puncho.sislelabs.com", group: "staging" },
        { short: "web", host: "web.puncho.sislelabs.com", add: ["app.puncho.app"] },
        { short: "operator", host: "operator.puncho.sislelabs.com", internal: true },
        { short: "site", host: "site.puncho.sislelabs.com", add: ["puncho.app", "www.puncho.app"] },
      ]),
    ).toBe("puncho.app");
  });

  it("scubatony: the service named like the project, not internal-system", () => {
    expect(
      realCase("scubatony", [
        { short: "scubatony", host: "scubatony.sislelabs.com" },
        { short: "internal-system", host: "internal-system.scubatony.sislelabs.com" },
      ]),
    ).toBe("scubatony.sislelabs.com");
  });

  it("the project base domain wins over another apex", () => {
    expect(
      realCase(
        "shop",
        [
          { short: "docs", host: "docs.shop.kuso.app", add: ["shopdocs.io"] },
          { short: "store", host: "store.shop.kuso.app", add: ["shop.com"] },
        ],
        "shop.com",
      ),
    ).toBe("shop.com");
  });
});

describe("projectStatsLabel", () => {
  it("builds the production strip with uptime", () => {
    const r = projectStatsLabel({
      env: "production",
      cpuMillicores: 100,
      memBytes: 390 * 1024 * 1024,
      pods: 6,
      uptime: { up: 3, total: 3 },
    });
    expect(r.scope).toBe("Production");
    expect(r.parts.map((p) => p.key)).toEqual(["cpu", "mem", "pods", "uptime"]);
    expect(r.text).toBe("Production · 100 mCPU · Mem 390 MiB · 6 service pods · 3/3 uptime checks up");
    expect(r.parts[0].title).toMatch(/production service.*Addons and cron jobs excluded/);
  });

  it("names a non-production group and drops empty uptime", () => {
    const r = projectStatsLabel({ env: "staging", cpuMillicores: 1500, memBytes: 0, pods: 1, uptime: { up: 0, total: 0 } });
    expect(r.text).toBe("staging · 1.5 cores · Mem 0 MiB · 1 service pod");
    expect(r.parts[2].title).toMatch(/every staging service/);
  });
});

describe("serviceIssues", () => {
  const st = (kind: string, tone: string) => ({ kind, tone });
  it("counts failing, stuck and degraded services from the node statuses", () => {
    const r = serviceIssues([st("live", "success"), st("crashing", "error"), st("build_failed", "error"), st("stuck", "error"), st("degraded", "warning")]);
    expect(r).toMatchObject({ failing: 2, stuck: 1, degraded: 1, tone: "error" });
    expect(r.text).toBe("2 services failing · 1 stuck · 1 degraded");
  });
  it("names the noun on the first part only and stays quiet when healthy", () => {
    expect(serviceIssues([st("stuck", "error")]).text).toBe("1 service stuck");
    expect(serviceIssues([st("degraded", "warning")]).tone).toBe("warning");
    expect(serviceIssues([st("live", "success"), st("sleeping", "muted")]).text).toBeUndefined();
  });
});

describe("projectHealthLine", () => {
  const row = (short: string, kind: string, tone: string) => ({ short, status: { kind, tone } });
  it("counts a failing service the ping never reaches", () => {
    const r = projectHealthLine(
      [row("web", "live", "success"), row("api", "live", "success"), row("worker", "crashing", "error")],
      [
        { service: "web", state: "up" },
        { service: "api", state: "up" },
      ],
    );
    expect(r.text).toBe("2 of 3 services checked · 1 failing");
    expect(r.tone).toBe("error");
    expect(r.problems).toEqual(["worker"]);
  });
  it("folds a failing uptime check into the same failing count", () => {
    const r = projectHealthLine(
      [row("web", "live", "success"), row("img", "stuck", "error")],
      [
        { service: "web", state: "up" },
        { service: "img", state: "down" },
      ],
    );
    expect(r.text).toBe("2 of 2 services checked · 1 failing");
  });
  it("says all up only when nothing needs attention", () => {
    expect(projectHealthLine([row("web", "live", "success")], [{ service: "web", state: "up" }]).text).toBe(
      "1 of 1 service checked · all up",
    );
  });
  it("names the noun when uptime is off", () => {
    const r = projectHealthLine([row("web", "rollout_failing", "warning"), row("api", "crashing", "error")], null);
    expect(r.text).toBe("1 service failing · 1 degraded");
    expect(r.tone).toBe("error");
    expect(r.problems).toEqual(["api", "web"]);
  });
  it("has nothing to say for a healthy unchecked project", () => {
    expect(projectHealthLine([row("web", "live", "success")], null).text).toBeUndefined();
  });
});

describe("lastDeployedAt mid-rollout", () => {
  it("holds the last settled value until the new pods are ready", () => {
    const settled = env("hold", { deployed: "2026-11-01T10:00:00Z" });
    settled.status = { ...settled.status, state: "running", rolloutCompletedAt: "2026-11-09T09:58:00Z" };
    expect(lastDeployedAt(settled)).toBe("2026-11-09T09:58:00Z");
    // The rollout starts: the completion stamp is gone, the operator's is old.
    const rolling = env("hold", { deployed: "2026-11-01T10:00:00Z", state: "deploying" });
    expect(lastDeployedAt(rolling)).toBe("2026-11-09T09:58:00Z");
    const done = env("hold", { deployed: "2026-11-01T10:00:00Z", state: "running" });
    done.status = { ...done.status, rolloutCompletedAt: "2026-11-09T10:01:00Z" };
    expect(lastDeployedAt(done)).toBe("2026-11-09T10:01:00Z");
  });
});

describe("lastDeployedAt on a first deploy", () => {
  it("is undefined until the first pods are ready", () => {
    const e = env("firstdeploy", { promoted: "2026-11-09T10:00:00Z", deployed: "2026-11-09T10:00:00Z", state: "deploying" });
    e.status = { ...e.status, replicas: { ready: 0, desired: 1 } };
    expect(lastDeployedAt(e)).toBeUndefined();
    e.status = { ...e.status, state: "running", replicas: { ready: 1, desired: 1 }, rolloutCompletedAt: "2026-11-09T10:00:40Z" };
    expect(lastDeployedAt(e)).toBe("2026-11-09T10:00:40Z");
  });
});
