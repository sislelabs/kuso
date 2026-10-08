import { afterEach, describe, expect, it, vi } from "vitest";
import { applyConfig } from "./api";

function serve(body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(body), { status: 200 })),
  );
}

afterEach(() => vi.unstubAllGlobals());

// The server's spec.Plan leaves empty buckets nil, which encode as null.
const nullPlan = {
  servicesToCreate: ["web"],
  servicesToUpdate: null,
  servicesToDelete: null,
  addonsToCreate: null,
  addonsToUpdate: null,
  addonsToDelete: null,
  cronsToCreate: null,
  cronsToUpdate: null,
  cronsToDelete: null,
};

describe("applyConfig", () => {
  it("turns null plan buckets into empty arrays on a dry run", async () => {
    serve(nullPlan);
    const plan = await applyConfig("p", "x", true);
    expect(plan.servicesToCreate).toEqual(["web"]);
    expect(plan.addonsToUpdate).toEqual([]);
    expect(plan.cronsToDelete).toEqual([]);
  });

  it("normalises the plan inside an apply result", async () => {
    serve({ plan: nullPlan, errors: null });
    const res = await applyConfig("p", "x", false);
    expect(res.plan.addonsToUpdate).toEqual([]);
    expect(res.plan.servicesToDelete).toEqual([]);
    expect(res.errors).toBeUndefined();
  });
});
