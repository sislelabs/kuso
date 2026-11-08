import { describe, expect, it } from "vitest";
import { githubCommitUrl, githubCompareUrl, githubRepoSlug } from "./github";

describe("githubRepoSlug", () => {
  it("parses https, ssh, bare and .git forms", () => {
    expect(githubRepoSlug("https://github.com/ivo9999/kuso-demo")).toBe("ivo9999/kuso-demo");
    expect(githubRepoSlug("https://github.com/ivo9999/kuso-demo.git/")).toBe("ivo9999/kuso-demo");
    expect(githubRepoSlug("git@github.com:biznesguys/analiz.git")).toBe("biznesguys/analiz");
    expect(githubRepoSlug("github.com/a/b")).toBe("a/b");
  });

  it("rejects other hosts and look-alikes", () => {
    expect(githubRepoSlug("https://gitlab.com/a/b")).toBeNull();
    expect(githubRepoSlug("https://github.com.evil.io/a/b")).toBeNull();
    expect(githubRepoSlug("https://github.com/only-owner")).toBeNull();
    expect(githubRepoSlug(undefined)).toBeNull();
  });
});

describe("githubCompareUrl", () => {
  const repo = "https://github.com/o/r";
  it("links two real SHAs", () => {
    expect(githubCompareUrl(repo, "aaaaaaa1", "bbbbbbb2")).toBe("https://github.com/o/r/compare/aaaaaaa1...bbbbbbb2");
  });
  it("refuses synthetic refs and identical SHAs", () => {
    expect(githubCompareUrl(repo, "main-x7k2p9q", "bbbbbbb2")).toBeNull();
    expect(githubCompareUrl(repo, "aaaaaaa1", "aaaaaaa1")).toBeNull();
  });
  it("commit link needs a hex SHA", () => {
    expect(githubCommitUrl(repo, "abc1234")).toBe("https://github.com/o/r/commit/abc1234");
    expect(githubCommitUrl(repo, "main-x7k2p9q")).toBeNull();
  });
});
