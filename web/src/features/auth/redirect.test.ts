import { describe, expect, it } from "vitest";
import { safeRedirectTarget } from "./redirect";

const origin = "https://kuso.example.com";

describe("safeRedirectTarget", () => {
  it("keeps same-origin paths with query and hash", () => {
    expect(safeRedirectTarget("/projects/a?tab=logs#x", origin)).toBe("/projects/a?tab=logs#x");
  });

  it.each([
    ["tab", "/\t/evil.com"],
    ["newline", "/\n/evil.com"],
    ["carriage return", "/\r/evil.com"],
    ["protocol-relative", "//evil.com/x"],
    ["backslash", "/\\evil.com"],
    ["absolute", "https://evil.com/"],
    ["javascript", "javascript:alert(1)"],
    ["relative without slash", "projects"],
    ["empty", ""],
  ])("rejects %s", (_, raw) => {
    expect(safeRedirectTarget(raw, origin)).toBeNull();
  });

  it("rejects non-strings", () => {
    expect(safeRedirectTarget(null, origin)).toBeNull();
  });
});
