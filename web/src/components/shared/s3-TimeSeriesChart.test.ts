import { describe, expect, it } from "vitest";
import { niceTicks, seriesShape, timeTicks, yAxisWidth } from "./s3-TimeSeriesChart";
import type { Point } from "./s3-TimeSeriesChart";

describe("niceTicks", () => {
  it("lands on 1/2/5 steps and covers the max", () => {
    expect(niceTicks(0.06, 4)).toEqual([0, 0.02, 0.04, 0.06]);
    expect(niceTicks(95, 4)).toEqual([0, 50, 100]);
    expect(niceTicks(64, 4)).toEqual([0, 20, 40, 60, 80]);
    expect(niceTicks(0, 4)).toEqual([0, 1]);
  });
});

describe("timeTicks", () => {
  it("aligns to a clean interval inside the window", () => {
    const t0 = Date.UTC(2026, 10, 8, 10, 7);
    const ticks = timeTicks(t0, t0 + 3_600_000, 5);
    expect(ticks.length).toBeGreaterThan(1);
    expect(ticks.every((t) => t >= t0 && t <= t0 + 3_600_000)).toBe(true);
    expect(ticks[1] - ticks[0]).toBe(15 * 60_000);
  });
});

describe("yAxisWidth", () => {
  it("grows the gutter with the widest tick label", () => {
    expect(yAxisWidth(["0", "5"])).toBe(30);
    expect(yAxisWidth(["0", "512 MiB"])).toBeGreaterThan(yAxisWidth(["0", "5m"]));
    expect(yAxisWidth(["a".repeat(40)])).toBe(96);
    expect(yAxisWidth(["0", "100 mCPU"])).toBeGreaterThanOrEqual(70);
  });
});

describe("seriesShape", () => {
  const step = 30_000;
  const at = (i: number, v: number | null): Point => [i * step, v];

  it("breaks runs at nulls and time gaps instead of drawing them as zero", () => {
    const s = seriesShape([at(0, 5), at(1, 6), at(2, null), at(3, 7), at(4, 8), at(9, 9)], step * 2.5);
    expect(s.segments).toEqual([
      [[0, 5], [step, 6]],
      [[3 * step, 7], [4 * step, 8]],
      [[9 * step, 9]],
    ]);
    expect(s.samples).toBe(5);
  });

  it("calls a few isolated or paired samples sparse", () => {
    // The p95 shape behind the old "vertical bars": pairs scattered across the hour.
    const pairs = [0, 1, 20, 21, 40, 41, 80, 81].map((i) => at(i, 95));
    expect(seriesShape(pairs, step * 2.5).sparse).toBe(true);
    expect(seriesShape([at(0, 1), at(1, 1), at(2, 1)], step * 2.5).sparse).toBe(true);
    expect(seriesShape([], step).sparse).toBe(false);
  });

  it("calls a mostly continuous series dense", () => {
    const pts = Array.from({ length: 30 }, (_, i) => at(i, i % 7));
    pts.push(at(50, 3));
    expect(seriesShape(pts, step * 2.5).sparse).toBe(false);
    expect(seriesShape(pts, step * 2.5, [0, 40 * step]).sparse).toBe(false);
  });

  it("calls short plateaus that cover little of the window sparse", () => {
    // Live p95 over 1h: four runs of 4–8 points, 20 of 120 slots.
    const runs = [0, 1, 2, 3, 9, 10, 11, 12, 13, 14, 15, 16, 34, 35, 36, 37, 40, 41, 42, 43].map((i) => at(i, 95));
    expect(seriesShape(runs, step * 2.5).sparse).toBe(false);
    expect(seriesShape(runs, step * 2.5, [0, 120 * step]).sparse).toBe(true);
  });
});
