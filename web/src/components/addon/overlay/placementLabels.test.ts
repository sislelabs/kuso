import { describe, expect, it } from "vitest";
import { nodeMatchesLabels, placementLabels } from "./placementLabels";

describe("placementLabels", () => {
  it("keeps presence-only rules and drops keyless rows", () => {
    expect(
      placementLabels([
        { key: "ssd", value: "" },
        { key: " region ", value: " eu " },
        { key: "", value: "x" },
      ]),
    ).toEqual({ ssd: "", region: "eu" });
  });

  it("round-trips a stored presence-only map unchanged (not dirty on open)", () => {
    const stored = { ssd: "" };
    const rows = Object.entries(stored).map(([key, value]) => ({ key, value }));
    expect(JSON.stringify(placementLabels(rows))).toBe(JSON.stringify(stored));
  });
});

describe("nodeMatchesLabels", () => {
  const rules = [{ key: "ssd", value: "" }, { key: "region", value: "eu" }];

  it("matches a blank value on key presence", () => {
    expect(nodeMatchesLabels({ ssd: "true", region: "eu" }, rules)).toBe(true);
  });

  it("rejects a node missing the presence-only key", () => {
    expect(nodeMatchesLabels({ region: "eu" }, rules)).toBe(false);
  });

  it("rejects a value mismatch", () => {
    expect(nodeMatchesLabels({ ssd: "", region: "us" }, rules)).toBe(false);
  });
});
