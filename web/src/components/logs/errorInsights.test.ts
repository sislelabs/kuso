import { describe, expect, it } from "vitest";
import { buildLiveAt, plural, traceFrames } from "./errorInsights";

const b = (id: string, finishedAt: string, status = "succeeded") => ({ id, status, finishedAt, commitSha: id });

describe("buildLiveAt", () => {
  const builds = [b("c3", "2026-11-08T12:00:00Z"), b("c2", "2026-11-08T10:00:00Z"), b("cx", "2026-11-08T11:00:00Z", "failed")];
  const windowStart = Date.parse("2026-11-08T00:00:00Z");

  it("picks the newest successful build finished before the error", () => {
    const r = buildLiveAt(builds, "2026-11-08T11:30:00Z", windowStart);
    expect(r?.build.id).toBe("c2");
    expect(r?.afterMs).toBe(90 * 60_000);
  });

  it("gives up when the error predates every build or sits at the window edge", () => {
    expect(buildLiveAt(builds, "2026-11-08T09:00:00Z", windowStart)).toBeNull();
    expect(buildLiveAt(builds, "2026-11-08T00:05:00Z", windowStart)).toBeNull();
  });
});

describe("traceFrames", () => {
  it("returns the frame lines after the sample", () => {
    const lines = ["noise", "TypeError: x is undefined", "    at render (app.js:10:5)", "", "    at main (index.js:2:1)", "next"];
    expect(traceFrames(lines, "TypeError: x is undefined", 2)).toEqual(["at render (app.js:10:5)", "at main (index.js:2:1)"]);
  });

  it("is empty when the sample isn't in the archive window", () => {
    expect(traceFrames(["a", "b"], "c")).toEqual([]);
  });
});

describe("plural", () => {
  it("handles one and many", () => {
    expect(plural(1, "occurrence")).toBe("1 occurrence");
    expect(plural(1200, "occurrence")).toBe("1,200 occurrences");
  });
});
