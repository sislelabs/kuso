import { describe, expect, it } from "vitest";
import { parseRate } from "./CostRatesSection";

describe("parseRate", () => {
  it("reads decimals and treats blank as zero", () => {
    expect(parseRate("0.04")).toBe(0.04);
    expect(parseRate(".5")).toBe(0.5);
    expect(parseRate(" ")).toBe(0);
  });
  it("rejects anything that isn't a non-negative number", () => {
    expect(parseRate("-1")).toBeNull();
    expect(parseRate("1e3")).toBeNull();
    expect(parseRate("abc")).toBeNull();
  });
});
