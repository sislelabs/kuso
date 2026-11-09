import { describe, expect, it } from "vitest";
import type { BuildSummary } from "@/features/services";
import type { KusoEnvironment } from "@/types/projects";
import { firstRunState } from "./firstRun";

const env = (status: KusoEnvironment["status"], branch = "main"): KusoEnvironment => ({
  metadata: { name: "shop-web-production" },
  spec: { service: "shop-web", branch } as KusoEnvironment["spec"],
  status,
});
const build = (status: string, startedAt: string, extra: Partial<BuildSummary> = {}): BuildSummary => ({
  id: startedAt,
  serviceName: "web",
  branch: "main",
  status,
  startedAt,
  ...extra,
});

describe("firstRunState", () => {
  it("reads no_image with a failed first build as never, failed", () => {
    const r = firstRunState(env({ state: "no_image" }), [build("failed", "2026-11-01T00:00:00Z")]);
    expect(r).toMatchObject({ kind: "never", failed: true, building: false });
  });

  it("picks the newest build regardless of list order", () => {
    const r = firstRunState(env({ state: "no_image" }), [
      build("failed", "2026-11-01T00:00:00Z"),
      build("running", "2026-11-02T00:00:00Z"),
    ]);
    expect(r).toMatchObject({ kind: "never", building: true, failed: false });
  });

  it("treats a failed build on a service that ran before as ran", () => {
    expect(firstRunState(env({ state: "build_failed", imageTag: "abc" }), [build("failed", "2026-11-01T00:00:00Z")]).kind).toBe("ran");
  });

  it("treats a running service as ran", () => {
    expect(firstRunState(env({ state: "running" }), []).kind).toBe("ran");
  });
});
