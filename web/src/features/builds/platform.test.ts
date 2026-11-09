import { describe, expect, it } from "vitest";
import { isPlatformFailure, stallHeadline, stallNote } from "./platform";

describe("isPlatformFailure", () => {
  it("reads the classification, or the message on archived builds", () => {
    expect(isPlatformFailure({ failureClass: { kind: "build_platform" } })).toBe(true);
    expect(isPlatformFailure({ errorMessage: "kuso couldn't start the build: builder image unavailable after 10m0s (env-detect)" })).toBe(true);
    expect(isPlatformFailure({ failureClass: { kind: "lockfile_drift" }, errorMessage: "npm ci failed" })).toBe(false);
    expect(isPlatformFailure({})).toBe(false);
  });
});

describe("stallHeadline", () => {
  it("says kuso can't start the build for builder-side stalls", () => {
    expect(stallHeadline({ kind: "image_pull", summary: "Builder image can't be pulled" })).toBe(
      "kuso can't start the build: builder image unavailable",
    );
    expect(stallHeadline({ kind: "config_error", summary: "The build pod can't start: a secret or config value the container needs is missing" })).toBe(
      "kuso can't start the build: a secret or config value the container needs is missing",
    );
    expect(stallHeadline({ kind: "unschedulable", summary: "Waiting for a node: the node is cordoned" })).toBe(
      "Waiting for a node: the node is cordoned",
    );
  });
});

describe("stallNote", () => {
  const now = Date.parse("2026-11-09T12:00:00Z");
  it("counts down to the server's give-up time", () => {
    expect(stallNote({ failsAt: "2026-11-09T12:07:30Z" }, now)).toBe(
      "A problem on kuso's side, not in your code. If it doesn't recover, kuso stops the build in 8 min.",
    );
    expect(stallNote({ failsAt: "2026-11-09T11:59:00Z" }, now)).toMatch(/kuso is stopping the build\.$/);
  });
  it("leaves the countdown out for servers that don't send one", () => {
    expect(stallNote({}, now)).toBe("A problem on kuso's side, not in your code.");
  });
});
