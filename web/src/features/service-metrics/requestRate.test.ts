import { describe, expect, it } from "vitest";
import { fmtRequestCount, requestScale } from "./requestRate";

describe("requestScale", () => {
  it("counts per bucket below one request a second", () => {
    expect(requestScale(0.02, 30_000)).toEqual({ perBucket: true, bucketSec: 30, unit: "per 30s" });
    expect(requestScale(0.5, 300_000)).toEqual({ perBucket: true, bucketSec: 300, unit: "per 5m" });
    expect(requestScale(0.5, 3_600_000).unit).toBe("per 1h");
  });
  it("keeps a per-second rate at or above 1/s, or with no data", () => {
    expect(requestScale(1, 30_000).perBucket).toBe(false);
    expect(requestScale(12, 30_000).unit).toBe("/s");
    expect(requestScale(null, 30_000).perBucket).toBe(false);
  });
});

describe("fmtRequestCount", () => {
  it("keeps a trickle visible instead of rounding it to 0", () => {
    expect(fmtRequestCount(0.6)).toBe("0.6");
    expect(fmtRequestCount(0.04)).toBe("<0.1");
    expect(fmtRequestCount(3)).toBe("3");
    expect(fmtRequestCount(1234)).toBe("1.2K");
    expect(fmtRequestCount(0)).toBe("0");
  });
});
