import { describe, expect, it } from "vitest";
import { compareVersions, unavailableNotice } from "./unavailable";

describe("compareVersions", () => {
  it("orders numerically, not lexically", () => {
    expect(compareVersions("v0.36.0", "v0.9.1")).toBeGreaterThan(0);
    expect(compareVersions("0.36.0", "v0.36.0")).toBe(0);
    expect(compareVersions("v0.35.9", "v0.36.0")).toBeLessThan(0);
    expect(compareVersions("dev", "v0.36.0")).toBeNull();
  });
});

describe("unavailableNotice", () => {
  it("never asks an up-to-date server to upgrade", () => {
    expect(unavailableNotice("History", { since: "v0.36.0", version: "v0.36.0" })).toBe(
      "History isn't available on this server (kuso v0.36.0).",
    );
  });
  it("says 'yet' with the version on an older server", () => {
    expect(unavailableNotice("History", { since: "v0.36.0", version: "v0.35.2" })).toBe(
      "History isn't available from this server yet (kuso v0.35.2).",
    );
  });
  it("copes without a known version", () => {
    expect(unavailableNotice("History", { version: null })).toBe("History isn't available from this server yet.");
  });
});
