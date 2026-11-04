import { describe, expect, it } from "vitest";
import { applySubscriptionDelta } from "./subscriptionDelta";

describe("applySubscriptionDelta", () => {
  it("keeps a key another user added since this tab loaded", () => {
    // Tab saw [A]; someone added X; this tab adds B.
    expect(applySubscriptionDelta(["A", "X"], ["A"], ["A", "B"])).toEqual(["A", "B", "X"]);
  });

  it("removes only what this edit removed", () => {
    expect(applySubscriptionDelta(["A", "B", "X"], ["A", "B"], ["B"])).toEqual(["B", "X"]);
  });
});
