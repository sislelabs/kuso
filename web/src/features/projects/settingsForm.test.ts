import { describe, expect, it } from "vitest";
import { formFromSpec, sectionDirty, sectionPatch } from "./settingsForm";

const base = formFromSpec({
  description: "shop",
  baseDomain: "shop.example.com",
  defaultRepo: { url: "https://github.com/acme/shop", defaultBranch: "main" },
  previews: { enabled: false, ttlDays: 7 },
  requestLimits: { maxConcurrent: 500 },
});

describe("project settings cards", () => {
  it("sends only the changed fields of the saved card", () => {
    const form = { ...base, description: " Storefront ", alwaysOn: true };
    expect(sectionDirty("general", form, base)).toBe(true);
    expect(sectionDirty("scaling", form, base)).toBe(true);
    expect(sectionDirty("previews", form, base)).toBe(false);
    expect(sectionPatch("general", form, base)).toEqual({ ok: true, body: { description: "Storefront" } });
    expect(sectionPatch("scaling", form, base)).toEqual({ ok: true, body: { alwaysOn: true } });
  });

  it("clears the default repo when its URL is emptied", () => {
    const form = { ...base, repoURL: "" };
    expect(sectionPatch("general", form, base)).toEqual({ ok: true, body: { clearDefaultRepo: true } });
  });

  it("rejects a malformed limit with the field's name", () => {
    const form = { ...base, limitRate: "1.5" };
    expect(sectionPatch("scaling", form, base)).toEqual({
      ok: false,
      error: "Requests per second must be a whole number, or -1 for no limit.",
    });
    const ok = sectionPatch("scaling", { ...base, limitRate: "-1", limitMaxConcurrent: "" }, base);
    expect(ok).toEqual({ ok: true, body: { requestLimits: { maxConcurrent: 0, ratePerSecond: -1, burst: 0 } } });
  });
});
