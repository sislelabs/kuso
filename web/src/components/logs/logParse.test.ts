import { describe, expect, it } from "vitest";
import { buildMatcher, detectLevel, parseAnsi, parseJsonLine, splitMatches, stripAnsi } from "./logParse";

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
