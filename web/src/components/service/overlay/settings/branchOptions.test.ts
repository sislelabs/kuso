import { describe, expect, it } from "vitest";
import { filterBranches, nextActive } from "./branchOptions";

const names = ["main", "develop", "feature/main-nav", "release/1.2", "hotfix-main", "Maintenance"];

describe("filterBranches", () => {
  it("lists every branch for an empty query", () => {
    expect(filterBranches(names, "  ")).toEqual({ shown: names, more: 0 });
  });

  it("puts prefix matches before inner matches, case-insensitively", () => {
    expect(filterBranches(names, "MAIN").shown).toEqual(["main", "Maintenance", "feature/main-nav", "hotfix-main"]);
  });

  it("caps the list and counts what it left out", () => {
    const many = Array.from({ length: 120 }, (_, i) => `b${i}`);
    const out = filterBranches(many, "", 50);
    expect(out.shown).toHaveLength(50);
    expect(out.more).toBe(70);
  });

  it("returns nothing for a name the remote doesn't have", () => {
    expect(filterBranches(names, "nope")).toEqual({ shown: [], more: 0 });
  });
});

describe("nextActive", () => {
  it("starts at the first or last option and wraps", () => {
    expect(nextActive(-1, 3, "ArrowDown")).toBe(0);
    expect(nextActive(-1, 3, "ArrowUp")).toBe(2);
    expect(nextActive(2, 3, "ArrowDown")).toBe(0);
    expect(nextActive(0, 3, "ArrowUp")).toBe(2);
    expect(nextActive(0, 0, "ArrowDown")).toBe(-1);
  });
});
