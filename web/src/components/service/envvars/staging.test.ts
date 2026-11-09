import { describe, expect, it } from "vitest";
import type { Row } from "@/components/service/envVarTransforms";
import { applyStagedEnv, isEmptyDraft, isRowStaged, rowStageMarker, toStagedEnv } from "./staging";

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

describe("row stage markers", () => {
  const staged = { set: [{ name: "REVIEW_FLAG", value: "on", before: null, sensitive: false }], unset: [] };

  it("keeps a stored row marked staged while another row's error blocks staging", () => {
    const stored = isRowStaged(row("REVIEW_FLAG", "on", { origName: undefined }), staged);
    expect(stored).toBe(true);
    expect(rowStageMarker({ changed: true, staged: stored, blocked: true })).toBe("staged");
  });

  it("says an edit the error kept out of the store is not staged", () => {
    const stored = isRowStaged(row("REVIEW_FLAG", "off"), staged);
    expect(stored).toBe(false);
    expect(rowStageMarker({ changed: true, staged: stored, blocked: true })).toBe("not-staged");
    expect(rowStageMarker({ changed: true, staged: stored, blocked: false })).toBe("staged");
    expect(rowStageMarker({ changed: false, staged: false, blocked: true })).toBeNull();
  });

  it("treats a new value-less row as a draft, but not a deployed or staged one", () => {
    expect(isEmptyDraft(row("SHARED_TOKEN", "", { origName: undefined }), undefined)).toBe(true);
    expect(isEmptyDraft(row("", "", { origName: undefined }), undefined)).toBe(true);
    expect(isEmptyDraft(row("SHARED_TOKEN", "x", { origName: undefined }), undefined)).toBe(false);
    // Deployed rows (origName set) and blank values staged on purpose stay.
    expect(isEmptyDraft(row("SECRET_KEY", "", { secretBacked: true }), undefined)).toBe(false);
    const blank = { set: [{ name: "EMPTY_OK", value: "", before: null, sensitive: false }], unset: [] };
    expect(isEmptyDraft(row("EMPTY_OK", "", { origName: undefined }), blank)).toBe(false);
  });
});
