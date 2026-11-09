import { describe, expect, it } from "vitest";
import type { Row } from "@/components/service/envVarTransforms";
import { applyStagedEnv, toStagedEnv } from "./staging";

const row = (name: string, value: string, extra: Partial<Row> = {}): Row => ({
  id: name,
  name,
  value,
  fromSecret: false,
  secretBacked: false,
  visible: true,
  origName: name,
  ...extra,
});

describe("env staging", () => {
  it("round-trips a plan through the staged form", () => {
    const base = [row("A", "1"), row("B", "2"), row("SECRET_KEY", "", { secretBacked: true })];
    const staged = toStagedEnv({ valueWrites: [{ name: "A", value: "9" }, { name: "C", value: "3" }], deletes: ["B"] }, base, [])!;
    expect(staged.set).toEqual([
      { name: "A", value: "9", before: "1", sensitive: false },
      { name: "C", value: "3", before: null, sensitive: false },
    ]);
    expect(staged.unset).toEqual([{ name: "B", before: "2", sensitive: false }]);
    const rows = applyStagedEnv(base, staged);
    expect(rows.map((r) => `${r.name}=${r.value}`)).toEqual(["A=9", "C=3", "SECRET_KEY="]);
  });

  it("returns undefined for an empty plan", () => {
    expect(toStagedEnv({ valueWrites: [], deletes: [] }, [], [])).toBeUndefined();
  });
});
