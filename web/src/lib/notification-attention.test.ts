import { describe, expect, it } from "vitest";
import { badgeCount, collapseUpdates, needsAttention, unreadBellCount } from "./notification-attention";

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

describe("unreadBellCount", () => {
  const unread = [
    { id: 4, type: "build.succeeded", severity: "info" },
    { id: 3, type: "update.available", severity: "info" },
    { id: 2, type: "build.failed", severity: "error", readAt: "2026-11-09T00:00:00Z" },
    { id: 1, type: "pod.crashed", severity: "error" },
  ];
  it("counts only unread rows the Needs-attention view shows", () => {
    expect(unreadBellCount(unread, true, true, new Set())).toBe(2);
    expect(unreadBellCount(unread, false, true, new Set())).toBe(1);
    expect(unreadBellCount(unread, false, true, new Set([1]))).toBe(0);
  });
  it("leaves a lone informational event out of the attention badge", () => {
    expect(unreadBellCount([{ id: 9, type: "build.succeeded", severity: "info" }], false, true, new Set())).toBe(0);
    expect(unreadBellCount([{ id: 9, type: "build.succeeded", severity: "info" }], false, false, new Set())).toBe(1);
  });
});

describe("badgeCount", () => {
  it("matches the panel's unread count up to 99", () => {
    expect(badgeCount(7)).toBe("7");
    expect(badgeCount(42)).toBe("42");
    expect(badgeCount(140)).toBe("99+");
  });
});
