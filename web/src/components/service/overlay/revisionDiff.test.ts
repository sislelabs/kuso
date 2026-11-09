import { describe, expect, it } from "vitest";
import type { Revision } from "@/features/revisions";
import { changedFields, formatValue, revisionFieldChanges } from "./revisionDiff";

const rev = (id: string, createdAt: string, snapshot: unknown): Revision => ({
  id,
  project: "shop",
  kind: "service",
  name: "web",
  createdAt,
  snapshot,
});

describe("revisionFieldChanges", () => {
  it("flattens a settings PATCH and pairs it with the newest older value", () => {
    const history = [
      rev("1", "2026-11-01T00:00:00Z", { patch: { port: 3000, scale: { min: 1 } } }),
      rev("2", "2026-11-02T00:00:00Z", { patch: { port: 8080 } }),
      rev("3", "2026-11-03T00:00:00Z", { patch: { port: 9090, scale: { min: 2, max: 4 } } }),
    ];
    expect(revisionFieldChanges(history[2], history)).toEqual([
      { field: "port", before: 8080, after: 9090 },
      { field: "scale.min", before: 1, after: 2 },
      { field: "scale.max", before: undefined, after: 4 },
    ]);
  });

  it("skips op metadata on non-patch snapshots", () => {
    expect([...changedFields({ op: "environment.branch", env: "staging", branch: "dev" }).keys()]).toEqual(["branch"]);
  });

  it("never prints a masked value", () => {
    expect(formatValue("••••••••")).toBe("hidden");
  });
});
