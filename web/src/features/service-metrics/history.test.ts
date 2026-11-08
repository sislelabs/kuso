import { beforeEach, describe, expect, it } from "vitest";
import { getSamples, mergeSamples, recordSample, resetSamplesForTest } from "./history";
import { parseCpuQuantity, stepToMs } from "./api";

describe("recordSample", () => {
  beforeEach(() => resetSamplesForTest());
  it("sums pods, dedupes repeat metrics-server timestamps, keeps per env", () => {
    const pods = [
      { pod: "a", timestamp: "2026-11-08T10:00:00Z", cpuMillicores: 10, memBytes: 100 },
      { pod: "b", timestamp: "2026-11-08T10:00:00Z", cpuMillicores: 5, memBytes: 50 },
    ];
    const now = Date.parse("2026-11-08T10:00:10Z");
    const s1 = recordSample("e", pods, now);
    expect(s1).toEqual([{ t: Date.parse("2026-11-08T10:00:00Z"), cpuMillicores: 15, memBytes: 150, pods: 2 }]);
    expect(recordSample("e", pods, now + 5000)).toBe(s1);
    recordSample("e", pods.map((p) => ({ ...p, timestamp: "2026-11-08T10:00:15Z" })), now + 15000);
    expect(getSamples("e")).toHaveLength(2);
    expect(getSamples("other")).toEqual([]);
  });
});

describe("mergeSamples", () => {
  const at = (t: number) => ({ t, cpuMillicores: t, memBytes: t, pods: 1 });
  it("appends only live polls newer than the last server point", () => {
    expect(mergeSamples([at(10), at(40)], [at(30), at(40), at(55)]).map((s) => s.t)).toEqual([10, 40, 55]);
  });
  it("uses the live polls alone when the server has none", () => {
    const live = [at(1)];
    expect(mergeSamples([], live)).toBe(live);
  });
});

describe("quantities", () => {
  it("parses cpu and steps", () => {
    expect(parseCpuQuantity("500m")).toBe(500);
    expect(parseCpuQuantity("1.5")).toBe(1500);
    expect(parseCpuQuantity("x")).toBeUndefined();
    expect(stepToMs("30s")).toBe(30_000);
    expect(stepToMs("2m")).toBe(120_000);
  });
});
