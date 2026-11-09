import { describe, expect, it } from "vitest";
import { errorCountLabel, errorsUnavailable } from "./errors";

describe("errorCountLabel", () => {
  it("states the count, unit and window the same way everywhere", () => {
    expect(errorCountLabel(1)).toBe("1 error · 24h");
    expect(errorCountLabel(0)).toBe("0 errors · 24h");
    expect(errorCountLabel(1234, "7d")).toBe("1,234 errors · 7d");
  });
});

describe("errorsUnavailable", () => {
  it("is true only when the summary is missing and nothing fell back", () => {
    expect(errorsUnavailable(true, 0, 0)).toBe(true);
    expect(errorsUnavailable(true, 2, 2)).toBe(true);
    expect(errorsUnavailable(true, 2, 1)).toBe(false);
    expect(errorsUnavailable(false, 0, 0)).toBe(false);
  });
});
