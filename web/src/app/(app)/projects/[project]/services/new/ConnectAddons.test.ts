import { describe, expect, it } from "vitest";
import { connectedByDefault, subscriptionFor } from "./ConnectAddons";

describe("subscriptionFor", () => {
  const existing = [
    { short: "cache", kind: "redis" },
    { short: "db", kind: "postgres" },
  ];
  const byDefault = (a: { kind: string }) => connectedByDefault(a.kind);
  it("connects databases and leaves caches off by default", () => {
    expect(subscriptionFor(existing, byDefault, [])).toEqual(["db"]);
  });
  it("leaves the server default when every addon is connected", () => {
    expect(subscriptionFor(existing, () => true, ["mysql"])).toBeUndefined();
    expect(subscriptionFor([], byDefault, ["postgres"])).toBeUndefined();
  });
  it("adds addons created with the service to an explicit list", () => {
    expect(subscriptionFor(existing, () => false, ["postgres"])).toEqual(["postgres"]);
  });
});
