import { describe, expect, it } from "vitest";
import { fromSvc } from "./_primitives";
import { buildServicePatch } from "./buildServicePatch";
import { SECTION_FIELDS, dirtySections, discardSection, scopedState } from "./sectionFields";

describe("SECTION_FIELDS", () => {
  it("maps every form field to exactly one section", () => {
    const fields = Object.keys(fromSvc(undefined)).sort();
    const mapped = Object.values(SECTION_FIELDS).flat();
    expect([...mapped].sort()).toEqual(fields);
    expect(new Set(mapped).size).toBe(mapped.length);
  });
});

describe("per-section save", () => {
  const base = fromSvc(undefined);
  const edited = { ...base, port: "3000", scaleMax: "9", buildArgs: "A=1" };

  it("reports each edited card as dirty", () => {
    expect(dirtySections(edited, base)).toEqual(["networking", "scale", "build"]);
  });

  it("saving one card sends only that card's fields", () => {
    const res = buildServicePatch(scopedState(["scale"], edited, base), base, {});
    expect("body" in res && Object.keys(res.body)).toEqual(["scale"]);
  });

  it("discarding one card keeps the other cards' edits", () => {
    const next = discardSection("scale", edited, base);
    expect(next.scaleMax).toBe(base.scaleMax);
    expect(next.port).toBe("3000");
    expect(dirtySections(next, base)).toEqual(["networking", "build"]);
  });
});
