import { describe, expect, it } from "vitest";
import { summarizeUptime, type UptimeServiceStatus } from "./api";

const s = (state: string): UptimeServiceStatus => ({ service: state, env: "e", state, url: "" });

describe("summarizeUptime", () => {
  it("buckets up/failing/down and lumps the rest", () => {
    expect(summarizeUptime([s("up"), s("up"), s("failing"), s("down"), s("paused"), s("pending")])).toEqual({
      up: 2,
      failing: 1,
      down: 1,
      other: 2,
    });
  });
});
