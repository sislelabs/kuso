import { describe, expect, it } from "vitest";

import { parseWatchPathsText } from "./watchPaths";

describe("parseWatchPathsText", () => {
  it("splits on newlines and commas, trimming blanks", () => {
    expect(parseWatchPathsText(" apps/web/**\n\npackages/ui/**, kuso.yml \n")).toEqual([
      "apps/web/**",
      "packages/ui/**",
      "kuso.yml",
    ]);
  });
  it("returns an empty list for blank input (clears to the default)", () => {
    expect(parseWatchPathsText("  \n ")).toEqual([]);
  });
});
