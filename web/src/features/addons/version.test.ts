import { describe, expect, it } from "vitest";
import { addonVersion, addonVersionLabel } from "./version";

describe("addonVersion", () => {
  it("prefers the running version, cleaned of image flavours", () => {
    expect(addonVersion("15", "16-alpine")).toBe("16");
    expect(addonVersion("16", undefined)).toBe("16");
    expect(addonVersion(undefined, "7.2.4-bookworm")).toBe("7.2.4");
    expect(addonVersion(undefined, "v1.2")).toBe("1.2");
    expect(addonVersion()).toBe("");
  });
  it("labels with the kind", () => {
    expect(addonVersionLabel("Postgres", "", "16-alpine")).toBe("Postgres 16");
    expect(addonVersionLabel("Redis")).toBe("Redis");
  });
});
