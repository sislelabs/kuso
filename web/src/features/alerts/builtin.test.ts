import { describe, expect, it } from "vitest";
import type { LatestDelivery, NotificationChannel } from "./api";
import { builtinAlertRows, deliveriesForRule, lastDelivery, routeSummary, routingFor } from "./builtin";

const ch = (over: Partial<NotificationChannel>): NotificationChannel => ({
  id: "c",
  name: "ops",
  type: "discord",
  enabled: true,
  pipelines: [],
  events: [],
  ...over,
});

const dl = (over: Partial<LatestDelivery>): LatestDelivery => ({
  eventType: "build.failed",
  channel: "c1",
  channelName: "ops",
  channelType: "discord",
  status: "delivered",
  at: "2026-11-09T10:00:00Z",
  ...over,
});

describe("routingFor", () => {
  const channels = [
    ch({ id: "all", name: "everything" }),
    ch({ id: "builds", name: "builds", events: ["build.failed"] }),
    ch({ id: "crash", name: "crashes", events: ["pod.crashed"] }),
    ch({ id: "shop", name: "shop-team", events: ["build.failed"], pipelines: ["shop"] }),
    ch({ id: "off", name: "paused", events: ["build.failed"], enabled: false }),
  ];

  it("treats an empty event list as every event and skips other events", () => {
    const r = routingFor("build.failed", channels);
    expect(r.routes.map((x) => x.channel)).toEqual(["all", "builds", "shop"]);
    expect(r.paused).toBe(1);
  });

  it("applies project filters when the event has a project", () => {
    expect(routingFor("build.failed", channels, "blog").routes.map((x) => x.channel)).toEqual(["all", "builds"]);
    expect(routingFor("build.failed", channels, "shop").routes.map((x) => x.channel)).toEqual(["all", "builds", "shop"]);
  });

  it("tolerates null lists from the server", () => {
    const loose = { ...ch({ id: "x" }), events: null, pipelines: null } as unknown as NotificationChannel;
    expect(routingFor("pod.crashed", [loose]).routes).toHaveLength(1);
  });
});

describe("routeSummary", () => {
  it("names channels and their project limits", () => {
    const r = routingFor("build.failed", [ch({ name: "ops" }), ch({ id: "s", name: "shop-team", pipelines: ["shop"] })]);
    expect(routeSummary(r)).toBe("ops, shop-team (shop only)");
  });

  it("says when nothing receives it, and why", () => {
    expect(routeSummary({ routes: [], paused: 0 })).toBe("Not delivered to any channel");
    expect(routeSummary({ routes: [], paused: 2 })).toBe("Not delivered: its 2 channels are paused");
  });
});

describe("builtinAlertRows", () => {
  it("leaves out alert.fired (that's the rules) and keeps rule deliveries off built-in rows", () => {
    const rows = builtinAlertRows(
      [
        { type: "build.failed", label: "Build failed", group: "build", defaultMention: "@here" },
        { type: "alert.fired", label: "Alert fired", group: "runtime", defaultMention: "" },
      ],
      [ch({})],
      [dl({}), dl({ eventType: "alert.fired", ruleId: "r1" })],
    );
    expect(rows.map((r) => r.type)).toEqual(["build.failed"]);
    expect(rows[0].deliveries).toHaveLength(1);
  });
});

describe("deliveriesForRule", () => {
  it("matches the rule's alert.fired deliveries only", () => {
    const ds = [dl({ eventType: "alert.fired", ruleId: "r1" }), dl({ eventType: "alert.fired", ruleId: "r2" }), dl({})];
    expect(deliveriesForRule(ds, "r1")).toHaveLength(1);
  });
});

describe("lastDelivery", () => {
  it("is null with nothing recorded", () => {
    expect(lastDelivery([])).toBeNull();
  });

  it("reports the newest success", () => {
    const s = lastDelivery([dl({ channelName: "old", at: "2026-11-08T10:00:00Z" }), dl({ channelName: "new" })]);
    expect(s).toEqual({ ok: true, text: "Last delivered to new", at: "2026-11-09T10:00:00Z" });
  });

  it("puts a failing channel ahead of newer successes", () => {
    const s = lastDelivery([
      dl({ channelName: "slack", status: "failed", error: "DNS lookup failed", at: "2026-11-08T10:00:00Z" }),
      dl({ channelName: "ops" }),
    ]);
    expect(s?.ok).toBe(false);
    expect(s?.text).toBe("Last delivery to slack failed: DNS lookup failed");
  });
});
