import { describe, expect, it } from "vitest";
import { describeCron, formatAbsoluteTime, formatTimeRange } from "./format-time";

describe("describeCron", () => {
  it.each([
    ["0 23 * * *", "Daily at 23:00 UTC"],
    ["30 9 * * 1", "Mondays at 09:30 UTC"],
    ["0 9 * * 1-5", "Weekdays at 09:00 UTC"],
    ["0 3 1 * *", "Monthly on the 1st at 03:00 UTC"],
    ["0 3 22 * *", "Monthly on the 22nd at 03:00 UTC"],
    ["5 * * * *", "Hourly at :05"],
    ["*/15 * * * *", "Every 15 minutes"],
    ["0 */6 * * *", "Every 6 hours at :00 (UTC)"],
    ["0 0 1 1 *", "0 0 1 1 * (UTC)"],
    ["", "—"],
  ])("%s → %s", (expr, want) => {
    expect(describeCron(expr)).toBe(want);
  });
});

describe("formatAbsoluteTime", () => {
  it("names the zone", () => {
    expect(formatAbsoluteTime("2026-11-09T23:00:00Z", { timeZone: "UTC" })).toBe("Nov 9, 23:00 UTC");
    expect(formatAbsoluteTime("2026-11-09T23:00:00Z", { timeZone: "UTC", date: false, zone: false })).toBe("23:00");
    expect(formatAbsoluteTime("2026-11-09T23:00:00Z")).toMatch(/^Nov (9|10), \d\d:\d\d \S+/);
  });
});

describe("formatTimeRange", () => {
  it("names the zone once and drops the end date within a day", () => {
    expect(formatTimeRange("2026-11-09T09:00:00Z", "2026-11-09T10:30:00Z", { timeZone: "UTC" })).toBe("Nov 9, 09:00 – 10:30 UTC");
  });
});
