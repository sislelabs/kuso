import { describe, expect, it } from "vitest";
import { fetchProjectAlerts, type FeedEvent } from "./projectAlerts";

const ev = (id: number, project: string): FeedEvent => ({ id, type: "build.failed", title: "t", project, createdAt: "2026-11-09T07:00:00Z" });

describe("fetchProjectAlerts", () => {
  it("asks the server to filter and makes one call when it did", async () => {
    const calls: string[] = [];
    const out = await fetchProjectAlerts("shop", async (p) => {
      calls.push(p);
      return Array.from({ length: 30 }, (_, i) => ev(i, "shop"));
    });
    expect(calls).toEqual(["/api/notifications/my-feed?limit=30&project=shop"]);
    expect(out).toHaveLength(30);
  });

  it("filters client-side and digs deeper when an older server ignored the filter", async () => {
    const calls: string[] = [];
    const out = await fetchProjectAlerts("shop", async (p) => {
      calls.push(p);
      const n = p.includes("limit=200") ? 200 : 30;
      return Array.from({ length: n }, (_, i) => ev(i, i % 20 === 0 ? "shop" : "blog"));
    });
    expect(calls).toHaveLength(2);
    expect(calls[1]).toBe("/api/notifications/my-feed?limit=200&project=shop");
    expect(out.every((e) => e.project === "shop")).toBe(true);
    expect(out).toHaveLength(10);
  });
});
