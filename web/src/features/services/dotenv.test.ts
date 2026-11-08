import { describe, expect, it } from "vitest";
import { looksSecret, parseDotenv } from "./dotenv";

const pairs = (text: string) => parseDotenv(text).vars.map((v) => [v.name, v.value]);

describe("parseDotenv", () => {
  it("parses plain, exported, quoted and commented lines", () => {
    const text = [
      "# leading comment",
      "",
      "A=1",
      "export B = two words ",
      "C='single # not a comment'",
      'D="line\\nbreak \\"q\\""',
      "E=value # trailing comment",
      "F=abc#def",
      "G=",
      "\r",
    ].join("\r\n");
    expect(pairs(text)).toEqual([
      ["A", "1"],
      ["B", "two words"],
      ["C", "single # not a comment"],
      ["D", 'line\nbreak "q"'],
      ["E", "value"],
      ["F", "abc#def"],
      ["G", ""],
    ]);
    expect(parseDotenv(text).errors).toEqual([]);
  });

  it("keeps single-quoted escapes literal", () => {
    expect(pairs("A='x\\ny'")).toEqual([["A", "x\\ny"]]);
  });

  it("joins a double-quoted value across lines and keeps line numbers", () => {
    const r = parseDotenv('KEY="-----BEGIN\nabc\n-----END"\nNEXT=1');
    expect(r.vars).toEqual([
      { name: "KEY", value: "-----BEGIN\nabc\n-----END", line: 1 },
      { name: "NEXT", value: "1", line: 4 },
    ]);
  });

  it("reports malformed lines with their line numbers", () => {
    const r = parseDotenv(["OK=1", "no equals here", "1BAD=x", "=x", "PORT=80", "OK=2", 'Q="open', "Z=1"].join("\n"));
    expect(r.vars.map((v) => v.name)).toEqual(["OK"]);
    expect(r.errors.map((e) => e.line)).toEqual([2, 3, 4, 5, 6, 7]);
    expect(r.errors[0].message).toMatch(/KEY=VALUE/);
    expect(r.errors[4].message).toMatch(/line 1/);
    expect(r.errors[5].message).toMatch(/unterminated/);
  });

  it("rejects text after a closing quote", () => {
    expect(parseDotenv('A="x" y').errors).toHaveLength(1);
    expect(parseDotenv('A="x" # ok').errors).toHaveLength(0);
  });
});

describe("looksSecret", () => {
  it("flags secret-looking names and credentialed URLs", () => {
    expect(looksSecret("STRIPE_SECRET_KEY", "x")).toBe(true);
    expect(looksSecret("GITHUB_TOKEN", "x")).toBe(true);
    expect(looksSecret("REDIS", "redis://u:p@host:6379")).toBe(true);
    expect(looksSecret("LOG_LEVEL", "debug")).toBe(false);
    expect(looksSecret("PUBLIC_URL", "https://example.com")).toBe(false);
  });
});
