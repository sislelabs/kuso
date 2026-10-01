import { describe, expect, it } from "vitest";
import { sqlCellDisplay } from "./SQLTab";

describe("sqlCellDisplay", () => {
  it("never renders an empty string as NULL", () => {
    expect(sqlCellDisplay("", false)).toEqual({ kind: "empty" });
    expect(sqlCellDisplay("", undefined)).toEqual({ kind: "ambiguous" });
  });

  it("renders NULL only when the server says the cell is NULL", () => {
    expect(sqlCellDisplay("", true)).toEqual({ kind: "null" });
  });

  it("truncates long text", () => {
    const d = sqlCellDisplay("x".repeat(250), false);
    expect(d.kind === "text" && d.text.length).toBe(201);
  });
});
