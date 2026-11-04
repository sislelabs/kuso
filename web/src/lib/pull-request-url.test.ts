import { describe, expect, it } from "vitest";
import { githubHttpsUrl, pullRequestUrl } from "./pull-request-url";

describe("pullRequestUrl", () => {
  it("builds the PR link from an https repo URL with .git", () => {
    expect(pullRequestUrl("https://github.com/acme/shop.git", 12)).toBe(
      "https://github.com/acme/shop/pull/12",
    );
  });

  it("handles scp-style URLs and strips credentials", () => {
    expect(pullRequestUrl("git@github.com:acme/shop.git", 3)).toBe("https://github.com/acme/shop/pull/3");
    expect(pullRequestUrl("https://x:tok@github.com/acme/shop", 3)).toBe("https://github.com/acme/shop/pull/3");
  });

  it("returns undefined for non-GitHub repos or a missing PR number", () => {
    expect(pullRequestUrl("https://gitlab.com/acme/shop", 3)).toBeUndefined();
    expect(pullRequestUrl("https://github.com/acme/shop", undefined)).toBeUndefined();
  });
});

describe("githubHttpsUrl", () => {
  it("keeps github.com https links and drops everything else", () => {
    expect(githubHttpsUrl("https://github.com/o/r/pull/3")).toBe("https://github.com/o/r/pull/3");
    for (const u of ["http://github.com/o/r/pull/3", "https://github.com.evil.io/x", "https://evil.io/github.com/", "javascript:alert(1)", ""]) {
      expect(githubHttpsUrl(u)).toBeUndefined();
    }
  });
});
