import { describe, expect, it } from "vitest";
import { collapseRuns } from "./collapse-runs";

describe("collapseRuns", () => {
  const key = (r: { a: string }) => r.a;

  it("folds only consecutive repeats", () => {
    const rows = [{ a: "x", n: 1 }, { a: "x", n: 2 }, { a: "y", n: 3 }, { a: "x", n: 4 }];
    const runs = collapseRuns(rows, key);
    expect(runs.map((r) => [r.item.n, r.count, r.last.n])).toEqual([
      [1, 2, 2],
      [3, 1, 3],
      [4, 1, 4],
    ]);
  });

  it("handles empty input", () => {
    expect(collapseRuns([], key)).toEqual([]);
  });
});
