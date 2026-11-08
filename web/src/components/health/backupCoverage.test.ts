import { describe, expect, it } from "vitest";
import { addonBackupCoverageText, addonBackupFailingText, summarizeAddonBackups } from "./backupCoverage";

const row = (covered: boolean, healthy = true) => ({ covered, healthy });

describe("summarizeAddonBackups", () => {
  it("counts every row in the denominator and failing only among covered", () => {
    // An uncovered row is healthy=true server-side, but even an unhealthy
    // uncovered one (tenant schedule that never runs) is a gap, not a failure.
    const c = summarizeAddonBackups([row(true), row(true, false), row(false), row(false, false)]);
    expect(c).toEqual({ total: 4, covered: 2, uncovered: 2, failing: 1 });
    expect(addonBackupCoverageText(c)).toBe("2 of 4 addons have no scheduled backup");
    expect(addonBackupFailingText(c)).toBe("1 of 2 scheduled addon backups failing");
  });

  it("reads naturally at the edges", () => {
    expect(addonBackupCoverageText(summarizeAddonBackups([]))).toBe("No addons to back up");
    expect(addonBackupCoverageText(summarizeAddonBackups([row(true), row(true)]))).toBe(
      "All 2 addons have a scheduled backup",
    );
    expect(addonBackupCoverageText(summarizeAddonBackups([row(false)]))).toBe("1 of 1 addon has no scheduled backup");
    expect(addonBackupFailingText(summarizeAddonBackups([row(true)]))).toBeUndefined();
  });
});
