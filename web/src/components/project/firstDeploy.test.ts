import { describe, expect, it } from "vitest";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import type { BuildSummary } from "@/features/services";
import { firstDeployProgress, pickFirstDeploy, type ServiceSnapshot } from "./firstDeploy";

const svc = (name: string, spec: Partial<KusoService["spec"]> = {}): KusoService =>
  ({
    metadata: { name: `p-${name}` },
    spec: { project: "p", repo: { url: "https://github.com/acme/app" }, runtime: "dockerfile", ...spec },
  }) as KusoService;

const env = (service: string, status: Record<string, unknown> = {}): KusoEnvironment =>
  ({
    metadata: { name: `p-${service}-production`, labels: { "kuso.sislelabs.com/env": "production" } },
    spec: { service: `p-${service}`, host: `${service}.p.example.com`, branch: "main" },
    status,
  }) as unknown as KusoEnvironment;

const build = (status: string): BuildSummary => ({ id: "b1", serviceName: "web", status }) as BuildSummary;

const states = (s: ServiceSnapshot) => firstDeployProgress("p", s).steps.map((st) => `${st.id}:${st.state}`);

describe("pickFirstDeploy", () => {
  it("follows a never-deployed public service before an internal one", () => {
    const snaps = [
      { service: svc("api", { internal: true }), env: env("api", { state: "no_image" }) },
      { service: svc("web"), env: env("web", { state: "no_image" }) },
    ];
    expect(pickFirstDeploy("p", snaps)?.service.metadata.name).toBe("p-web");
  });

  it("returns null once anything in the project has been live", () => {
    const snaps = [
      { service: svc("web"), env: env("web", { state: "no_image" }) },
      { service: svc("api"), env: env("api", { state: "running", ready: true, lastDeployedAt: "2026-11-01T00:00:00Z" }) },
    ];
    expect(pickFirstDeploy("p", snaps)).toBeNull();
  });

  it("ignores stopped services and empty projects", () => {
    expect(pickFirstDeploy("p", [])).toBeNull();
    expect(pickFirstDeploy("p", [{ service: svc("web", { stopped: true }) }])).toBeNull();
  });
});

describe("firstDeployProgress", () => {
  it("walks repo → build → healthy → live", () => {
    const web = svc("web");
    expect(states({ service: web, env: env("web", { state: "no_image" }) })).toEqual([
      "source:done",
      "build:todo",
      "healthy:todo",
      "live:todo",
    ]);
    expect(states({ service: web, env: env("web", { state: "no_image" }), build: build("running") })).toEqual([
      "source:done",
      "build:active",
      "healthy:todo",
      "live:todo",
    ]);
    expect(
      states({ service: web, env: env("web", { state: "deploying", imageTag: "abc" }), build: build("succeeded") }),
    ).toEqual(["source:done", "build:done", "healthy:active", "live:todo"]);
    const live = firstDeployProgress("p", {
      service: web,
      env: env("web", { state: "running", ready: true, imageTag: "abc" }),
      build: build("succeeded"),
    });
    expect(live.live).toBe(true);
    expect(live.url).toBe("https://web.p.example.com");
    expect(live.steps.every((st) => st.state === "done")).toBe(true);
  });

  it("marks a failed build and a crashing first rollout as failed", () => {
    const web = svc("web");
    const failedBuild = firstDeployProgress("p", { service: web, env: env("web"), build: build("failed") });
    expect(failedBuild.failed).toBe(true);
    expect(failedBuild.steps[1]).toMatchObject({ id: "build", state: "failed" });
    expect(
      states({ service: web, env: env("web", { state: "crashlooping", imageTag: "abc" }), build: build("succeeded") }),
    ).toEqual(["source:done", "build:done", "healthy:failed", "live:todo"]);
  });

  it("skips the build step for image services", () => {
    const img = svc("app", { runtime: "image", repo: undefined, image: { repository: "ghcr.io/acme/app", tag: "1" } });
    const p = firstDeployProgress("p", { service: img, env: env("app", { state: "deploying" }) });
    expect(p.steps.map((s) => s.id)).toEqual(["source", "healthy", "live"]);
    expect(p.steps[0]).toMatchObject({ label: "Image set", state: "done", detail: "ghcr.io/acme/app:1" });
  });
});
