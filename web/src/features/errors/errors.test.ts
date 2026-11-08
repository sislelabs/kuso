import { describe, expect, it } from "vitest";
import { errorCountLabel } from "./errors";

describe("errorCountLabel", () => {
  it("states the count, unit and window the same way everywhere", () => {
    expect(errorCountLabel(1)).toBe("1 error · 24h");
    expect(errorCountLabel(0)).toBe("0 errors · 24h");
    expect(errorCountLabel(1234, "7d")).toBe("1,234 errors · 7d");
  });
});
