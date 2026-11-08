import { describe, expect, it } from "vitest";
import type { KusoEnvironment, KusoProject, KusoService } from "@/types/projects";
import {
  formatCores,
  lastDeployedAt,
  projectLastDeployedAt,
  projectPrimaryDomain,
  projectStatsLabel,
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
    expect(projectPrimaryDomain(project, services, envs)).toEqual({ host: "shop.com", href: "https://shop.com" });
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
    expect(projectPrimaryDomain(project, [svc("web", { domains: [{ host: "shop.io", tls: true }] })], [e])).toEqual({
      host: "shop.io",
      href: "https://shop.io",
    });
  });

  it("is undefined when nothing is public", () => {
    expect(projectPrimaryDomain(project, [svc("worker", { port: undefined })], [env("worker")])).toBeUndefined();
    expect(projectPrimaryDomain(project, [], [])).toBeUndefined();
  });
});

describe("formatCores", () => {
  it("stays in cores across the 1000m boundary", () => {
    expect(formatCores(0)).toBe("0 cores");
    expect(formatCores(4)).toBe("<0.01 cores");
    expect(formatCores(100)).toBe("0.1 cores");
    expect(formatCores(142)).toBe("0.14 cores");
    expect(formatCores(998)).toBe("1 core");
    expect(formatCores(1000)).toBe("1 core");
    expect(formatCores(1400)).toBe("1.4 cores");
    expect(formatCores(12000)).toBe("12 cores");
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
    expect(r.text).toBe("Production · CPU 0.1 cores · Mem 390 MiB · 6 pods · 3/3 uptime checks up");
    expect(r.parts[0].title).toMatch(/production service.*Addons and cron jobs excluded/);
  });

  it("names a non-production group and drops empty uptime", () => {
    const r = projectStatsLabel({ env: "staging", cpuMillicores: 1500, memBytes: 0, pods: 1, uptime: { up: 0, total: 0 } });
    expect(r.text).toBe("staging · CPU 1.5 cores · Mem 0 MiB · 1 pod");
    expect(r.parts[2].title).toMatch(/every staging service/);
  });
});
