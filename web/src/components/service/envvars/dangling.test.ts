import { describe, expect, it } from "vitest";
import { danglingRef } from "./dangling";

const addons = new Set(["db"]);

describe("danglingRef", () => {
  it("flags an empty addon-injected pooler key", () => {
    expect(danglingRef({ name: "POOLER_URL", value: "", addon: "db" }, addons)).toEqual({ addon: "db", key: "POOLER_URL", pooler: true, isRef: false });
  });
  it("flags a reference that resolved empty, only for known addons", () => {
    expect(danglingRef({ name: "DB", value: "${{ db.POOLER_URL }}", resolved: "" }, addons)?.isRef).toBe(true);
    expect(danglingRef({ name: "DB", value: "${{ api.URL }}", resolved: "" }, addons)).toBeNull();
    expect(danglingRef({ name: "DB", value: "${{ db.DATABASE_URL }}", resolved: "postgres://x" }, addons)).toBeNull();
    expect(danglingRef({ name: "DB", value: "${{ db.DATABASE_URL }}" }, addons)).toBeNull();
  });
});
