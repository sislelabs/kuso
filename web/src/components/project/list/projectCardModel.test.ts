import { describe, expect, it } from "vitest";
import type { KusoEnvironment, KusoProject, KusoService } from "@/types/projects";
import type { ProjectSummaryItem } from "@/features/projects";
import { buildCardModel, matchesQuery, rollupStatus } from "./projectCardModel";

const project: KusoProject = {
  metadata: { name: "shop", creationTimestamp: "2026-01-01T00:00:00Z" },
  spec: { defaultRepo: { url: "https://deploy:secret@github.com/acme/shop.git" } },
};

function svc(short: string, extra: Partial<KusoService["spec"]> = {}, labels: Record<string, string> = {}): KusoService {
  return {
    metadata: { name: `shop-${short}`, labels: { "kuso.sislelabs.com/service": short, ...labels } },
    spec: { project: "shop", port: 8080, ...extra },
  } as KusoService;
}

function env(short: string, state: string, annotations: Record<string, string> = {}, group = "production"): KusoEnvironment {
  return {
    metadata: {
      name: `shop-${short}-${group}`,
      labels: { "kuso.sislelabs.com/env": group, "kuso.sislelabs.com/service": short },
      annotations,
    },
    spec: { service: `shop-${short}`, kind: "production", host: `${short}.shop.example.com` },
    status: { state, stateDetail: `${short} is ${state}` },
  } as unknown as KusoEnvironment;
}

function summary(services: KusoService[], environments: KusoEnvironment[]): ProjectSummaryItem {
  return {
    project,
    services,
    environments,
    metrics: { project: "shop", cpuMillicores: 0, memBytes: 0, pods: 0, envs: 0 },
  };
}

describe("rollupStatus", () => {
  it("lets one failing service outrank healthy ones", () => {
    expect(rollupStatus(["live", "failing", "live"])).toBe("failing");
  });
  it("reports stopped only when every service is stopped", () => {
    expect(rollupStatus(["stopped", "stopped"])).toBe("stopped");
    expect(rollupStatus(["stopped", "live"])).toBe("live");
  });
  it("reports empty for a project without services", () => {
    expect(rollupStatus([])).toBe("empty");
  });
});

describe("buildCardModel", () => {
  it("counts only production services and uses the newest promotion as last deploy", () => {
    const m = buildCardModel(
      project,
      summary(
        [svc("web"), svc("api"), svc("web-staging", {}, { "kuso.sislelabs.com/env": "staging" })],
        [
          env("web", "running", { "kuso.sislelabs.com/promoted-at": "2026-11-01T10:00:00Z" }),
          env("api", "running", { "kuso.sislelabs.com/promoted-at": "2026-11-03T10:00:00.5Z" }),
          env("web", "crashlooping", { "kuso.sislelabs.com/promoted-at": "2026-11-07T10:00:00Z" }, "staging"),
        ],
      ),
    );
    expect(m.status).toBe("live");
    expect(m.serviceCount).toBe(2);
    expect(m.liveCount).toBe(2);
    expect(m.lastDeployAt).toBe("2026-11-03T10:00:00.5Z");
  });

  it("treats a user stop as stopped, not as an outage", () => {
    const m = buildCardModel(project, summary([svc("web", { stopped: true })], [env("web", "stopped")]));
    expect(m.status).toBe("stopped");
    expect(m.readyOf).toBe(0);
  });

  it("surfaces the failing service's detail", () => {
    const m = buildCardModel(project, summary([svc("web"), svc("api")], [env("web", "running"), env("api", "build_failed")]));
    expect(m.status).toBe("failing");
    expect(m.detail).toBe("api is build_failed");
  });

  it("never shows repo credentials", () => {
    const m = buildCardModel(project, summary([], []));
    expect(m.repo).toBe("github.com/acme/shop.git");
    expect(m.repoHref).toBe("https://github.com/acme/shop");
  });

  it("has no health before the summary loads", () => {
    const m = buildCardModel(project, undefined);
    expect(m.hasSummary).toBe(false);
  });
});

describe("matchesQuery", () => {
  it("matches service names and domains, all terms required", () => {
    const m = buildCardModel(project, summary([svc("worker")], [env("worker", "running")]));
    expect(matchesQuery(m, "worker")).toBe(true);
    expect(matchesQuery(m, "acme worker")).toBe(true);
    expect(matchesQuery(m, "acme billing")).toBe(false);
  });
});
