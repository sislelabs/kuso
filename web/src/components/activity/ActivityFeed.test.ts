import { describe, expect, it } from "vitest";
import { localDayKey } from "./ActivityFeed";

describe("localDayKey", () => {
  it("uses the local calendar date, not the UTC one", () => {
    const t = new Date(2026, 9, 1, 0, 30); // 00:30 local on Oct 1
    expect(localDayKey(t)).toBe("2026-10-01");
  });
});
