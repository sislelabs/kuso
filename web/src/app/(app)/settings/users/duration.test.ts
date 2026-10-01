import { describe, expect, it } from "vitest";
import { toGoDuration } from "./duration";

describe("toGoDuration", () => {
  it("converts days to hours", () => {
    expect(toGoDuration("7d")).toBe("168h");
    expect(toGoDuration("30d")).toBe("720h");
  });
  it("folds trailing hours and keeps minutes", () => {
    expect(toGoDuration("1d12h")).toBe("36h");
    expect(toGoDuration("1d30m")).toBe("24h30m");
  });
  it("passes Go durations through", () => {
    expect(toGoDuration(" 168h ")).toBe("168h");
    expect(toGoDuration("")).toBe("");
  });
});
