import { describe, expect, it } from "vitest";
import { runningImageOf } from "./status";

describe("runningImageOf", () => {
  it("reads the version newer servers report", () => {
    expect(runningImageOf({ pods: [], runningImage: "postgres:16-alpine", runningVersion: "16-alpine" })).toEqual({
      runningImage: "postgres:16-alpine",
      runningVersion: "16-alpine",
    });
  });
  it("is empty for older servers and digest-only images", () => {
    expect(runningImageOf({ namespace: "kuso", pods: [] })).toEqual({});
    expect(runningImageOf({ runningImage: "postgres@sha256:abc", runningVersion: "" })).toEqual({
      runningImage: "postgres@sha256:abc",
    });
    expect(runningImageOf(undefined)).toEqual({});
  });
});
