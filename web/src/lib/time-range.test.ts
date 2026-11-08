import { describe, expect, it } from "vitest";
import { timeRangeLabel, timeRangeMs } from "./time-range";

describe("time-range presets", () => {
  it("converts presets including days to milliseconds", () => {
    expect(timeRangeMs("15m")).toBe(15 * 60_000);
    expect(timeRangeMs("24h")).toBe(86_400_000);
    expect(timeRangeMs("7d")).toBe(7 * 86_400_000);
    expect(timeRangeMs("7w")).toBeNull();
  });

  it("labels presets in words", () => {
    expect(timeRangeLabel("1h")).toBe("Last 1 hour");
    expect(timeRangeLabel("30d")).toBe("Last 30 days");
    expect(timeRangeLabel("custom")).toBe("custom");
  });
});
