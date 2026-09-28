import { describe, expect, it } from "vitest";
import { buildTriggerMessage } from "./buildTrigger";

describe("buildTriggerMessage", () => {
  it("reports the fresh message for a newly created build", () => {
    expect(buildTriggerMessage({ existing: false }, "Build triggered for web")).toBe(
      "Build triggered for web",
    );
  });

  it("treats a missing flag as a fresh build", () => {
    expect(buildTriggerMessage({}, "Build triggered")).toBe("Build triggered");
  });

  it("reports a coalesced trigger as already in progress", () => {
    expect(buildTriggerMessage({ existing: true }, "Build triggered for web", "web")).toBe(
      "A build for web is already in progress",
    );
    expect(buildTriggerMessage({ existing: true }, "Build triggered")).toBe(
      "A build is already in progress",
    );
  });
});
