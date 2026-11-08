import { describe, expect, it } from "vitest";
import { niceTicks, timeTicks } from "./s3-TimeSeriesChart";

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
