import { describe, expect, it } from "vitest";
import { buildMatcher, dayChange, detectLevel, parseAnsi, isCollectorNote, parseJsonLine, sortByTime, splitLeadingTimestamp, splitMatches, stripAnsi } from "./logParse";

describe("detectLevel", () => {
  it.each([
    ['{"level":"error","msg":"boom"}', "error"],
    ['{"level":50,"msg":"pino error"}', "error"],
    ['{"level":30,"msg":"pino info"}', "info"],
    ["time=2026 level=warn msg=slow", "warn"],
    ["[ERROR] db down", "error"],
    ["2026-11-08 12:00:01 WARN cache miss storm", "warn"],
    ["INFO  server listening on :8080", "info"],
    ["DEBUG resolving host", "debug"],
    ["panic: runtime error: index out of range", "error"],
    ["Traceback (most recent call last):", "error"],
    ["TypeError: x is not a function", "error"],
    ["    at Object.<anonymous> (/app/index.js:10:5)", "error"],
    ["\x1b[31mERROR\x1b[0m something", "error"],
    ["GET /healthz 200 1ms", "none"],
    ["user errored out of the wizard", "none"],
  ])("%s → %s", (line, want) => {
    expect(detectLevel(line)).toBe(want);
  });
});

describe("parseAnsi", () => {
  it("keeps plain text as one segment", () => {
    expect(parseAnsi("hello")).toEqual([{ text: "hello", style: {} }]);
  });
  it("applies and resets SGR colours", () => {
    const segs = parseAnsi("a\x1b[1;31mred\x1b[0mb");
    expect(segs).toEqual([
      { text: "a", style: {} },
      { text: "red", style: { bold: true, fg: "red" } },
      { text: "b", style: {} },
    ]);
  });
  it("handles 256 and truecolor", () => {
    expect(parseAnsi("\x1b[38;5;196mx")[0].style.fg).toBe("rgb(255,0,0)");
    expect(parseAnsi("\x1b[38;2;1;2;3mx")[0].style.fg).toBe("rgb(1,2,3)");
  });
  it("drops non-SGR escapes", () => {
    expect(parseAnsi("\x1b[2Kdone").map((s) => s.text).join("")).toBe("done");
    expect(stripAnsi("\x1b[32mok\x1b[39m")).toBe("ok");
  });
});

describe("parseJsonLine", () => {
  it("parses objects and rejects non-objects", () => {
    expect(parseJsonLine('{"a":1,"b":"x"}')).toEqual({ a: 1, b: "x" });
    expect(parseJsonLine("[1,2]")).toBeNull();
    expect(parseJsonLine("not json {")).toBeNull();
  });
});

describe("matching", () => {
  it("substring matcher is case-insensitive and literal", () => {
    const m = buildMatcher("a.b")!;
    expect(m.test("xA.Bx")).toBe(true);
    expect(m.test("axb")).toBe(false);
    expect(splitMatches("xa.bx A.B", m.re).filter((p) => p.match).map((p) => p.text)).toEqual(["a.b", "A.B"]);
  });
  it("regex matcher with leading slash; invalid regex → null", () => {
    expect(buildMatcher("/^GET")!.test("get /")).toBe(true);
    expect(buildMatcher("/(")).toBeNull();
  });
});

describe("sortByTime", () => {
  const row = (ts: string, cont = false, backlog = false) => ({ ts, cont, backlog });
  it("orders the whole buffer by time, keeping continuations with their head", () => {
    const rows = [
      row("2026-01-01T00:00:05Z"),
      row("2026-01-01T00:00:05Z", true),
      row("2026-01-01T00:00:01Z"),
      row("2026-01-01T00:01:00Z"),
      row("2026-01-01T00:00:30Z"),
    ];
    expect(sortByTime(rows).map((r) => `${r.ts}${r.cont ? "+" : ""}`)).toEqual([
      "2026-01-01T00:00:01Z",
      "2026-01-01T00:00:05Z",
      "2026-01-01T00:00:05Z+",
      "2026-01-01T00:00:30Z",
      "2026-01-01T00:01:00Z",
    ]);
  });
  it("keeps timeless lines after what preceded them and leaves sorted input alone", () => {
    const rows = [row("", false, true), row("2026-01-01T00:00:05Z"), row("", false, true), row("2026-01-01T00:00:01Z")];
    expect(sortByTime(rows)).toEqual([rows[0], rows[3], rows[1], rows[2]]);
    const sorted = [row("2026-01-01T00:00:01Z"), row("2026-01-01T00:00:02Z")];
    expect(sortByTime(sorted)).toBe(sorted);
  });
});

describe("isCollectorNote", () => {
  it("spots kubelet notes, prefixed by the server or raw", () => {
    expect(isCollectorNote("WARN kubelet: unable to retrieve container logs for containerd://abc")).toBe(true);
    expect(isCollectorNote("unable to retrieve container logs for containerd://abc")).toBe(true);
    expect(isCollectorNote('failed to try resolving symlinks in path "/var/log/pods/x": lstat: no such file')).toBe(true);
    expect(isCollectorNote("ERROR unable to retrieve user 42")).toBe(false);
  });
});

describe("splitLeadingTimestamp", () => {
  it("strips a zoned ISO prefix and returns its instant", () => {
    expect(splitLeadingTimestamp("2026-11-09T07:55:29Z INFO processed 8 orders", true)).toEqual({
      ts: "2026-11-09T07:55:29.000Z",
      rest: "INFO processed 8 orders",
    });
    expect(splitLeadingTimestamp("[2026-11-09 09:55:29,120+0200] boot", false)?.ts).toBe("2026-11-09T07:55:29.120Z");
  });
  it("strips a zone-less stamp only when the row has its own time", () => {
    expect(splitLeadingTimestamp("2026-11-09 07:55:29 WARN slow", true)).toEqual({ rest: "WARN slow" });
    expect(splitLeadingTimestamp("2026-11-09 07:55:29 WARN slow", false)).toBeNull();
    expect(splitLeadingTimestamp("GET / 200", true)).toBeNull();
    expect(splitLeadingTimestamp("2026-11-09T07:55:29Z", true)).toBeNull();
  });
});

describe("detectLevel plain-text errors", () => {
  it("flags an upper-case ERROR word after a prefix", () => {
    expect(detectLevel("worker-1 | job 42 | 2026-01-01 12:00:00 ERROR send failed")).toBe("error");
    expect(detectLevel("Traceback (most recent call last):")).toBe("error");
    expect(detectLevel("no errors found")).toBe("none");
  });
});

describe("dayChange", () => {
  it("labels the first line of a new local day only", () => {
    expect(dayChange(undefined, "2026-11-09T10:00:00")).toBeNull();
    expect(dayChange("2026-11-09T10:00:00", "2026-11-09T23:59:00")).toBeNull();
    expect(dayChange("2026-11-09T23:59:00", "2026-11-10T00:01:00")).toMatch(/10/);
    expect(dayChange("2026-11-09T23:59:00", "not a time")).toBeNull();
  });
});
