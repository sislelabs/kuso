import { describe, expect, it } from "vitest";
import { repoOwners, sortReposByPush } from "./repoList";

const r = (fullName: string, pushedAt?: string) => ({ installationId: 1, repo: { fullName, pushedAt } });

describe("sortReposByPush", () => {
  it("orders newest push first and puts undated repos last in input order", () => {
    const out = sortReposByPush([
      r("a/old", "2026-01-01T00:00:00Z"),
      r("a/undated-1"),
      r("b/new", "2026-11-01T00:00:00Z"),
      r("a/undated-2"),
    ]);
    expect(out.map((e) => e.repo.fullName)).toEqual(["b/new", "a/old", "a/undated-1", "a/undated-2"]);
  });

  it("keeps server order when no repo has a push time", () => {
    const input = [r("z/one"), r("a/two"), r("m/three")];
    expect(sortReposByPush(input).map((e) => e.repo.fullName)).toEqual(["z/one", "a/two", "m/three"]);
  });
});

describe("repoOwners", () => {
  it("lists distinct owners in list order", () => {
    expect(repoOwners([r("b/x"), r("a/y"), r("b/z")])).toEqual(["b", "a"]);
  });
});
