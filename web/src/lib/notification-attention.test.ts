import { describe, expect, it } from "vitest";
import { badgeCount, collapseUpdates, needsAttention } from "./notification-attention";

describe("needsAttention", () => {
  it("keeps failures, alerts, security and updates; drops successes", () => {
    expect(needsAttention({ type: "build.succeeded", severity: "info" })).toBe(false);
    expect(needsAttention({ type: "pod.recovered" })).toBe(false);
    expect(needsAttention({ type: "build.failed", severity: "warn" })).toBe(true);
    expect(needsAttention({ type: "pod.crashed", severity: "error" })).toBe(true);
    expect(needsAttention({ type: "alert.fired", severity: "info" })).toBe(true);
    expect(needsAttention({ type: "alert.fired", severity: "info", extra: { state: "resolved" } })).toBe(false);
    expect(needsAttention({ type: "update.available", severity: "info" })).toBe(true);
    expect(needsAttention({ type: "security.deploy_blocked", severity: "info" })).toBe(true);
  });
});

describe("collapseUpdates", () => {
  const feed = [
    { id: 5, type: "update.available" },
    { id: 4, type: "build.failed" },
    { id: 3, type: "update.available" },
  ];
  it("keeps only the newest update notice", () => {
    expect(collapseUpdates(feed, true).map((e) => e.id)).toEqual([5, 4]);
    expect(collapseUpdates(feed, undefined).map((e) => e.id)).toEqual([5, 4]);
  });
  it("drops update notices once the server is current", () => {
    expect(collapseUpdates(feed, false).map((e) => e.id)).toEqual([4]);
  });
});

describe("badgeCount", () => {
  it("matches the panel's unread count up to 99", () => {
    expect(badgeCount(7)).toBe("7");
    expect(badgeCount(42)).toBe("42");
    expect(badgeCount(140)).toBe("99+");
  });
});
