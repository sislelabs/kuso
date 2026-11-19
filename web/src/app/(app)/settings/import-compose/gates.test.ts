import { describe, expect, it } from "vitest";
import { composeApplyBlocked } from "./gates";

const none = { allowMissingEnvFiles: false, allowUnresolvedVars: false, allowEmptyAddons: false };
const all = { allowMissingEnvFiles: true, allowUnresolvedVars: true, allowEmptyAddons: true };

describe("composeApplyBlocked", () => {
  it("allows a conversion with no risks", () => {
    expect(composeApplyBlocked({ missingEnvFiles: 0, unresolvedVars: 0, emptyAddons: 0 }, none)).toBe(false);
  });
  it("blocks each risk until it is acknowledged", () => {
    for (const risks of [
      { missingEnvFiles: 1, unresolvedVars: 0, emptyAddons: 0 },
      { missingEnvFiles: 0, unresolvedVars: 2, emptyAddons: 0 },
      { missingEnvFiles: 0, unresolvedVars: 0, emptyAddons: 1 },
    ]) {
      expect(composeApplyBlocked(risks, none)).toBe(true);
      expect(composeApplyBlocked(risks, all)).toBe(false);
    }
  });
  it("one acknowledgement doesn't cover another risk", () => {
    expect(
      composeApplyBlocked(
        { missingEnvFiles: 0, unresolvedVars: 1, emptyAddons: 1 },
        { ...none, allowEmptyAddons: true },
      ),
    ).toBe(true);
  });
});
