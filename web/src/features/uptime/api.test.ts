import { describe, expect, it } from "vitest";
import { summarizeUptime, uptimeView, type UptimeServiceStatus } from "./api";

const s = (state: string): UptimeServiceStatus => ({ service: state, env: "e", state, url: "" });

describe("summarizeUptime", () => {
  it("buckets up/failing/down and lumps the rest", () => {
    expect(summarizeUptime([s("up"), s("up"), s("failing"), s("down"), s("paused"), s("pending")])).toEqual({
      up: 2,
      failing: 1,
      down: 1,
      other: 2,
      checked: 4,
    });
  });
});

describe("uptimeView", () => {
  it("reads one label per state", () => {
    expect(uptimeView(undefined)).toMatchObject({ state: "not_set_up", label: "Not set up" });
    expect(uptimeView({ ...s("up"), latencyMs: 1 })).toMatchObject({ state: "up", label: "OK · 1 ms" });
    expect(uptimeView(s("paused")).label).toBe("Paused");
    expect(uptimeView({ ...s("paused"), reason: "no-image" }).label).toBe("Not deployed");
    expect(uptimeView(s("pending")).state).toBe("unknown");
    expect(uptimeView({ ...s("down"), stale: true }).label).toBe("Down");
  });
});
