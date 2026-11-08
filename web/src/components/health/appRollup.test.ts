import { describe, expect, it } from "vitest";
import type { ProjectSummaryItem } from "@/features/projects";
import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import { buildAppRollup, verdict, type ProjectProbe } from "./appRollup";

function svc(project: string, short: string, extra: Partial<KusoService["spec"]> = {}): KusoService {
  return {
    metadata: { name: `${project}-${short}`, labels: { "kuso.sislelabs.com/service": short } },
    spec: { project, ...extra },
  } as KusoService;
}

function env(project: string, short: string, state: string, group = "production", detail?: string): KusoEnvironment {
  return {
    metadata: { name: `${project}-${short}-${group}`, labels: { "kuso.sislelabs.com/env": group } },
    spec: { project, service: `${project}-${short}`, kind: group.startsWith("preview") ? "preview" : "production" },
    status: { state, stateDetail: detail },
  } as KusoEnvironment;
}

function item(project: string, services: KusoService[], environments: KusoEnvironment[]): ProjectSummaryItem {
  return {
    project: { metadata: { name: project }, spec: {} },
    services,
    environments,
    metrics: { project, cpuMillicores: 0, memBytes: 0, pods: 0, envs: 0 },
  } as ProjectSummaryItem;
}

function addon(project: string, name: string, conditions: Array<{ type: string; status: string; message?: string }>, external = false): KusoAddon {
  return {
    metadata: { name: `${project}-${name}` },
    spec: { project, kind: "postgres", ...(external ? { external: { secretName: "x" } } : {}) },
    status: { conditions },
  } as KusoAddon;
}

const deployed = [{ type: "Deployed", status: "True" }];

describe("buildAppRollup", () => {
  it("counts states and links each problem to the tab that fixes it", () => {
    const items = [
      item(
        "shop",
        [svc("shop", "api"), svc("shop", "web"), svc("shop", "worker", { stopped: true }), svc("shop", "jobs")],
        [
          env("shop", "api", "crashlooping", "production", "back-off restarting"),
          env("shop", "web", "build_failed"),
          env("shop", "worker", "running"),
          env("shop", "jobs", "running"),
          env("shop", "jobs", "release_failed", "staging"),
          env("shop", "api", "crashlooping", "preview-pr-4"),
          env("shop", "web", "sleeping", "staging"),
        ],
      ),
    ];
    const r = buildAppRollup(items, []);
    expect(r.services).toMatchObject({ total: 6, running: 1, sleeping: 1, stopped: 1 });
    expect(r.services.problems.map((p) => [p.service, p.group, p.status.label, p.href])).toEqual([
      ["api", "production", "Failing", "/projects/shop?service=api&tab=logs"],
      ["web", "production", "Failing", "/projects/shop?service=web&tab=deployments"],
      ["jobs", "staging", "Degraded", "/projects/shop?env=staging&service=jobs&tab=deployments"],
    ]);
    expect(r.services.problems[0]).toMatchObject({ cause: "Crashlooping", detail: "back-off restarting" });
  });

  it("folds probes: uptime, errors (404 = unsupported), addons by release condition", () => {
    const probes: ProjectProbe[] = [
      {
        project: "shop",
        addons: [
          addon("shop", "db", deployed),
          addon("shop", "cache", [{ type: "ReleaseFailed", status: "True", message: "upgrade failed" }]),
          addon("shop", "queue", []),
          addon("shop", "ext", [], true),
        ],
        uptime: {
          enabled: true,
          services: [
            { service: "api", env: "shop-api-production", state: "down", url: "", statusCode: 502 },
            { service: "web", env: "shop-web-production", state: "up", url: "" },
            { service: "cron", env: "shop-cron-production", state: "paused", url: "" },
          ],
        },
        errors: {
          since: "24h",
          services: {
            api: { occurrences: 12, groups: 2, lastSeen: "2026-11-08T10:00:00Z" },
            web: { occurrences: 40, groups: 1, lastSeen: "2026-11-08T10:00:00Z" },
          },
        },
      },
      { project: "old", addons: [], uptime: null, errors: "unsupported" },
    ];
    const r = buildAppRollup([], probes);
    expect(r.uptime).toMatchObject({ enabled: true, checks: 3, up: 1, paused: 1, unavailable: ["old"] });
    expect(r.uptime.problems[0]).toMatchObject({ service: "api", detail: "HTTP 502", href: "/projects/shop?service=api&tab=logs" });
    expect(r.errors).toMatchObject({ supported: true, occurrences: 52, groups: 3, unavailable: [] });
    expect(r.errors.hotspots.map((h) => h.service)).toEqual(["web", "api"]);
    expect(r.addons).toMatchObject({ total: 4, live: 1, external: 1 });
    expect(r.addons.problems.map((p) => [p.addon, p.status.label, p.href])).toEqual([
      ["cache", "Failing", "/projects/shop?addon=shop-cache&tab=overview"],
      ["queue", "Provisioning", "/projects/shop?addon=shop-queue&tab=overview"],
    ]);
  });

  it("doesn't link errors of a service that no longer exists", () => {
    const r = buildAppRollup(
      [item("shop", [svc("shop", "api")], [env("shop", "api", "running")])],
      [
        {
          project: "shop",
          addons: [],
          uptime: null,
          errors: {
            since: "24h",
            services: {
              api: { occurrences: 1, groups: 1, lastSeen: "" },
              gone: { occurrences: 3, groups: 1, lastSeen: "" },
            },
          },
        },
      ],
    );
    expect(r.errors.occurrences).toBe(4);
    expect(r.errors.hotspots.map((h) => [h.service, h.href])).toEqual([
      ["gone", undefined],
      ["api", "/projects/shop?service=api&tab=errors"],
    ]);
  });

  it("marks errors unsupported only when every server 404s", () => {
    const r = buildAppRollup([], [{ project: "a", addons: [], uptime: null, errors: "unsupported" }]);
    expect(r.errors.supported).toBe(false);
  });
});

describe("verdict", () => {
  it("is one success line when nothing needs attention, even with reconcile clean", () => {
    const r = buildAppRollup(
      [item("shop", [svc("shop", "api"), svc("shop", "web")], [env("shop", "api", "running"), env("shop", "web", "running")])],
      [],
    );
    expect(verdict(r, { issues: 0, critical: 0 })).toEqual({ tone: "success", headline: "All 2 running services healthy" });
  });

  it("lists every kind of problem and escalates to danger on hard failures", () => {
    const r = buildAppRollup(
      [item("shop", [svc("shop", "api")], [env("shop", "api", "degraded")])],
      [
        {
          project: "shop",
          addons: [],
          uptime: { enabled: true, services: [{ service: "api", env: "e", state: "failing", url: "" }] },
          errors: null,
        },
      ],
    );
    expect(verdict(r, { issues: 2, critical: 0 })).toEqual({
      tone: "warning",
      headline: "1 service needs attention · 1 uptime check failing · 2 reconcile issues",
    });
    const hard = buildAppRollup([item("shop", [svc("shop", "api")], [env("shop", "api", "crashlooping")])], []);
    expect(verdict(hard).tone).toBe("danger");
    expect(verdict(buildAppRollup([], []), { issues: 1, critical: 1 })).toEqual({
      tone: "danger",
      headline: "1 reconcile issue",
    });
  });
});
