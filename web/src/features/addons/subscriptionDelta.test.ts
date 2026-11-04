import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { applySubscriptionDelta, putSubscriptionRebased } from "./subscriptionDelta";

describe("applySubscriptionDelta", () => {
  it("keeps a key another user added since this tab loaded", () => {
    // Tab saw [A]; someone added X; this tab adds B.
    expect(applySubscriptionDelta(["A", "X"], ["A"], ["A", "B"])).toEqual(["A", "B", "X"]);
  });

  it("removes only what this edit removed", () => {
    expect(applySubscriptionDelta(["A", "B", "X"], ["A", "B"], ["B"])).toEqual(["B", "X"]);
  });
});

describe("putSubscriptionRebased", () => {
  it("sends the read it was computed from as `expected` and re-reads after a 409", async () => {
    const reads = [["A"], ["A", "X"]];
    const puts: { next: string[]; expected: string[] }[] = [];
    await putSubscriptionRebased(
      async () => reads.shift() ?? [],
      (fresh) => [...fresh, "B"].sort(),
      async (next, expected) => {
        puts.push({ next, expected });
        if (puts.length === 1) throw new ApiError(409, { error: "changed" }, "409 Conflict");
      },
    );
    expect(puts).toEqual([
      { next: ["A", "B"], expected: ["A"] },
      { next: ["A", "B", "X"], expected: ["A", "X"] },
    ]);
  });

  it("does not retry other errors", async () => {
    let calls = 0;
    await expect(
      putSubscriptionRebased(
        async () => [],
        (fresh) => fresh,
        async () => {
          calls++;
          throw new ApiError(500, null, "500");
        },
      ),
    ).rejects.toThrow();
    expect(calls).toBe(1);
  });
});
