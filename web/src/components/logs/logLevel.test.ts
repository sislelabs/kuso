import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { detectLevel, foldBlocks, stripAnsi } from "./logLevel";
import type { FoldInput, LogLevel } from "./logLevel";

interface FixtureLine {
  line: string;
  level: LogLevel;
  errorscan: boolean;
  divergent?: string;
}

const fixture = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../../../server-go/internal/errorscan/testdata/levels.json", import.meta.url)), "utf8"),
) as { lines: FixtureLine[] };

describe("detectLevel against the shared errorscan fixture", () => {
  it.each(fixture.lines.map((l) => [l.line, l.level] as const))("%s → %s", (line, want) => {
    expect(detectLevel(line)).toBe(want);
  });

  it("calls every line the Errors tab counts an error line, unless it says why not", () => {
    const disagree = fixture.lines.filter((l) => l.errorscan && l.level !== "error" && !l.divergent);
    expect(disagree.map((l) => l.line)).toEqual([]);
  });
});

describe("foldBlocks", () => {
  const T = Date.parse("2026-11-09T10:06:04.154Z");
  const input = (text: string, t = T, pod = "web-1"): FoldInput => ({
    pod,
    t,
    level: detectLevel(text),
    json: false,
    text: stripAnsi(text),
  });

  it("lifts a printed object to the severity on one of its inner lines", () => {
    const lines = ["{", "  severity_local: 'ERROR',", "  severity: 'ERROR',", "  code: '42P01',", "}"].map((l) => input(l));
    const out = foldBlocks(lines);
    expect(out.map((r) => r.level)).toEqual(["error", "error", "error", "error", "error"]);
    expect(out.map((r) => r.cont)).toEqual([false, true, true, true, true]);
  });

  it("keeps a notice object at info", () => {
    const out = foldBlocks(["{", "  severity: 'NOTICE',", "  message: 'relation exists, skipping',", "}"].map((l) => input(l)));
    expect(out.every((r) => r.level === "info")).toBe(true);
  });

  it("folds a Python traceback and its final exception line under the ERROR head", () => {
    const lines = [
      "ERROR failed to send order receipt order_id=1012",
      "Traceback (most recent call last):",
      '  File "/app/jobs/emails.py", line 42, in send_receipt',
      "    smtp.send(msg)",
      "smtplib.SMTPServerDisconnected: Connection unexpectedly closed",
    ].map((l) => input(l));
    const out = foldBlocks(lines);
    expect(out.map((r) => r.level)).toEqual(["error", "error", "error", "error", "error"]);
    // The Traceback line has its own level, so it heads a new block.
    expect(out.map((r) => r.cont)).toEqual([false, false, true, true, true]);
  });

  it("folds JS stack frames even though each frame looks like an error on its own", () => {
    const out = foldBlocks(["TypeError: x is not a function", "    at Object.<anonymous> (/app/index.js:10:5)"].map((l) => input(l)));
    expect(out[1]).toEqual({ cont: true, level: "error", head: 0 });
  });

  it("folds Postgres DETAIL lines into the error above", () => {
    const out = foldBlocks(
      ["2026-11-09 10:00:00 UTC [42] ERROR:  duplicate key value", "DETAIL:  Key (id)=(1) already exists."].map((l) => input(l)),
    );
    expect(out[1]).toEqual({ cont: true, level: "error", head: 0 });
  });

  it("doesn't fold across pods or across time", () => {
    expect(foldBlocks([input("{"), input("  severity: 'ERROR',", T, "web-2")])[1].cont).toBe(false);
    expect(foldBlocks([input("{"), input("  severity: 'ERROR',", T + 5_000)])[1].cont).toBe(false);
    // An unlevelled line a moment later stays its own line after an info head.
    expect(foldBlocks([input("INFO ok"), input("done", T)])[1].cont).toBe(false);
  });
});
