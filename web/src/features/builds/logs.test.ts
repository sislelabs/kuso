import { describe, expect, it } from "vitest";
import { extractFailureLines, hintLineNumber } from "./logs";

describe("extractFailureLines", () => {
  it("returns buildkit's failure summary block when present", () => {
    const lines = [
      "#8 [deps 2/3] COPY package.json ./",
      "#9 1.042 npm notice New major version of npm available!",
      "#9 1.042 npm error Run \"npm help ci\" for more info",
      '#9 ERROR: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1',
      "------",
      " > [deps 3/3] RUN npm ci:",
      "1.042 npm error aliases: clean-install",
      "------",
      "Dockerfile:16",
      "  16 | >>> RUN npm ci",
      'error: failed to solve: process "/bin/sh -c npm ci" did not complete successfully: exit code: 1',
    ];
    const out = extractFailureLines(lines);
    expect(out[0]).toMatch(/^#9 ERROR:/);
    expect(out).toContain("Dockerfile:16");
    expect(out[out.length - 1]).toMatch(/^error: failed to solve/);
    expect(out.join("\n")).not.toMatch(/npm notice/);
  });

  it("falls back to error-looking lines with one line of context", () => {
    const lines = ["installing", "resolving deps", "Error: Cannot find module 'x'", "done", "npm notice update available"];
    expect(extractFailureLines(lines)).toEqual(["resolving deps", "Error: Cannot find module 'x'"]);
  });

  it("returns nothing for a clean log", () => {
    expect(extractFailureLines(["step 1", "step 2", "ok"])).toEqual([]);
  });
});

describe("hintLineNumber", () => {
  const log = ["#1 [internal] load build definition", "Cloning into '/workspace'...", "\x1b[31mfatal: Remote branch main not found in upstream origin\x1b[0m", "exit 128"];
  it("finds the quoted line in the shown log, 1-based", () => {
    expect(hintLineNumber(log, "fatal: Remote branch main not found in upstream origin")).toBe(3);
  });
  it("matches a truncated hint and returns 0 when the line isn't there", () => {
    expect(hintLineNumber(log, "fatal: Remote branch main…")).toBe(3);
    expect(hintLineNumber(log, "something else")).toBe(0);
    expect(hintLineNumber(log, "")).toBe(0);
  });
});
