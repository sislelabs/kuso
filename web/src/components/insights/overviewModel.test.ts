import { describe, expect, it } from "vitest";
import type { Insights, InsightsPoint, InsightsService } from "@/features/insights";
import { findOutliers, findWaste, liveFailedBuilds, memoryGrowth } from "./overviewModel";

const MiB = 1024 * 1024;
const until = 1_800_000_000;
const since = until - 86_400;
const series = (f: (i: number) => number, step = 600): InsightsPoint[] => {
  const out: InsightsPoint[] = [];
  for (let t = since; t <= until; t += step) out.push([t, f((t - since) / step)]);
  return out;
};
const svc = (o: Partial<InsightsService>): InsightsService => ({
  project: "p",
  service: "web",
  env: "p-web-production",
  requests: [],
  cpu: [],
  memory: [],
  p95: [],
  errorRate: [],
  pods: 1,
  ...o,
});
const data = (services: InsightsService[], extra: Partial<Insights> = {}): Insights => ({
  range: "24h",
  stepSeconds: 600,
  since: new Date(since * 1000).toISOString(),
  until: new Date(until * 1000).toISOString(),
  requestsAvailable: true,
  usageAvailable: true,
  services,
  deploys: [],
  failedBuilds: [],
  certProblems: [],
  ...extra,
});

describe("memoryGrowth", () => {
  it("sees a steady climb and ignores a single spike", () => {
    expect(memoryGrowth(series((i) => 200 * MiB + i * MiB))).toBeGreaterThan(0.5);
    expect(memoryGrowth(series((i) => (i === 70 ? 900 * MiB : 200 * MiB)))).toBeNull();
  });
});

describe("findOutliers", () => {
  it("flags a latency jump in the last hour and a pod near its memory limit", () => {
    const out = findOutliers(
      data([
        svc({ service: "slow", requests: series(() => 5), p95: series((i) => (i >= 140 ? 900 : 100)) }),
        // Same jump on a trickle of traffic: a cold start, not an outlier.
        svc({ service: "quiet", requests: series(() => 0.2), p95: series((i) => (i >= 140 ? 900 : 100)) }),
        svc({ service: "full", memory: series(() => 470 * MiB), resources: { memLimitBytes: 512 * MiB } }),
        svc({ service: "calm", p95: series(() => 120), memory: series(() => 100 * MiB), resources: { memLimitBytes: 512 * MiB } }),
      ]),
    );
    expect(out.map((o) => [o.label, o.kind])).toEqual([
      ["p / full", "Near memory limit"],
      ["p / slow", "Slow"],
    ]);
  });
});

describe("findWaste", () => {
  it("calls only public services idle and flags big unused reservations", () => {
    const out = findWaste(
      data([
        svc({ service: "site", requests: series(() => 0), memory: series(() => 40 * MiB), resources: { memRequestBytes: 1024 * MiB } }),
        svc({ service: "worker", requests: [], memory: series(() => 300 * MiB), resources: { memRequestBytes: 512 * MiB } }),
      ]),
      new Set(["p/site"]),
    );
    expect(out.map((w) => [w.label, w.kind])).toEqual([
      ["p / site", "Idle"],
      ["p / site", "Memory reserved"],
    ]);
  });
});

describe("liveFailedBuilds", () => {
  it("drops a failure a later deploy fixed and keeps one per service", () => {
    const at = (h: number) => new Date((until - h * 3600) * 1000).toISOString();
    const out = liveFailedBuilds({
      failedBuilds: [
        { project: "p", service: "web", at: at(5), status: "failed", commitMessage: "", error: "" },
        { project: "p", service: "api", at: at(2), status: "failed", commitMessage: "", error: "x" },
        { project: "p", service: "api", at: at(3), status: "failed", commitMessage: "", error: "y" },
      ],
      deploys: [{ project: "p", service: "web", at: at(4), commitSha: "", commitMessage: "" }],
    });
    expect(out.map((b) => [b.service, b.error])).toEqual([["api", "x"]]);
  });
});
