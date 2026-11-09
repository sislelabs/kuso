import { describe, expect, it } from "vitest";
import { trimRepeatedCause } from "./statusCopy";

describe("trimRepeatedCause", () => {
  it("drops the repeated cause and keeps the rest", () => {
    expect(trimRepeatedCause("Build failed", "latest build failed and nothing is running")).toBe("Nothing is running");
    expect(trimRepeatedCause("Build failed", "latest build failed")).toBeUndefined();
  });
  it("leaves unrelated details alone", () => {
    expect(trimRepeatedCause("Crashlooping", "OOMKilled (exit 137)")).toBe("OOMKilled (exit 137)");
    expect(trimRepeatedCause("Build failed", undefined)).toBeUndefined();
  });
});
