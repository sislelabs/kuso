import { describe, expect, it } from "vitest";
import { deployEffect, groupDeploys, seriesStats } from "./insightsModel";

describe("deployEffect", () => {
  // 24h range, 10m step: a one-hour window either side, skipping one step.
  const pts: [number, number][] = [];
  for (let t = 0; t <= 20_000; t += 600) pts.push([t, t < 10_000 ? 100 : 50]);

  it("compares the hour before with the hour after", () => {
    const e = deployEffect(pts, 10_000, 86_400, 600);
    expect(e).not.toBeNull();
    expect(e!.before).toBe(100);
    expect(e!.after).toBe(50);
    expect(e!.pct).toBe(-50);
  });

  it("returns null without data on both sides", () => {
    expect(deployEffect(pts, 400, 86_400, 600)).toBeNull();
  });
});

describe("groupDeploys", () => {
  it("folds one push into one row, newest first", () => {
    const g = groupDeploys([
      { project: "p", service: "web", at: "2026-11-19T10:00:00Z", commitSha: "abc", commitMessage: "m" },
      { project: "p", service: "api", at: "2026-11-19T10:01:00Z", commitSha: "abc", commitMessage: "m" },
      { project: "q", service: "web", at: "2026-11-19T11:00:00Z", commitSha: "abc", commitMessage: "m" },
    ]);
    expect(g.map((x) => [x.project, x.services])).toEqual([
      ["q", ["web"]],
      ["p", ["api", "web"]],
    ]);
  });
});

describe("seriesStats", () => {
  it("takes the last point as now", () => {
    expect(seriesStats([[1, 4], [2, 8], [3, 3]])).toEqual({ now: 3, avg: 5, peak: 8 });
  });
});
