import { describe, expect, it } from "vitest";
import { BELL_UNREAD_LIMIT, bellBadgeText, bellCount, bellUnreadLabel } from "./bellCount";

describe("bell count", () => {
  it("shows the exact count up to 9, then 9+", () => {
    expect(bellBadgeText(bellCount(1, 1))).toBe("1");
    expect(bellBadgeText(bellCount(9, 9))).toBe("9");
    expect(bellBadgeText(bellCount(10, 10))).toBe("9+");
    expect(bellBadgeText(bellCount(48, 48))).toBe("9+");
  });

  it("the panel label is the badge's underlying number", () => {
    const c = bellCount(48, 60);
    expect(bellUnreadLabel(c)).toBe("48 unread");
  });

  it("marks a truncated poll as a lower bound", () => {
    expect(bellUnreadLabel(bellCount(150, BELL_UNREAD_LIMIT))).toBe("150+ unread");
    expect(bellUnreadLabel(bellCount(150, BELL_UNREAD_LIMIT - 1))).toBe("150 unread");
  });
});
