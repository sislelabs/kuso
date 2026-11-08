import { describe, expect, it } from "vitest";
import { fromSvc, type FormState } from "./_primitives";
import { healthcheckPatch, secondsUntilRestart } from "./healthcheckPatch";
import type { KusoService } from "@/types/projects";

const withHc = (hc: NonNullable<KusoService["spec"]["healthcheck"]>): FormState =>
  fromSvc({ metadata: { name: "p-web" }, spec: { healthcheck: hc } } as unknown as KusoService);

describe("healthcheckPatch", () => {
  const off = fromSvc(undefined);

  it("sends nothing when the card is untouched", () => {
    expect(healthcheckPatch(off, off)).toEqual({});
  });

  it("turning it on sends the path and only the filled-in numbers", () => {
    const next = { ...off, hcEnabled: true, hcPath: " /healthz ", hcPeriod: "15" };
    expect(healthcheckPatch(next, off)).toEqual({ healthcheck: { path: "/healthz", periodSeconds: 15 } });
  });

  it("turning it off clears the check with an empty path", () => {
    const base = withHc({ path: "/health", periodSeconds: 10 });
    expect(healthcheckPatch({ ...base, hcEnabled: false }, base)).toEqual({ healthcheck: { path: "" } });
  });

  it("an edit resends the whole block (the server replaces it)", () => {
    const base = withHc({ path: "/health", port: 9000, failureThreshold: 5 });
    expect(healthcheckPatch({ ...base, hcPath: "/ready" }, base)).toEqual({
      healthcheck: { path: "/ready", port: 9000, failureThreshold: 5 },
    });
  });

  it.each([
    ["missing path", { hcPath: "" }],
    ["relative path", { hcPath: "health" }],
    ["protocol-relative path", { hcPath: "//x" }],
    ["bad port", { hcPort: "70000" }],
    ["zero interval", { hcPeriod: "0" }],
    ["fractional threshold", { hcThreshold: "2.5" }],
    ["timeout above default interval", { hcTimeout: "11" }],
  ])("rejects %s", (_, patch) => {
    const next = { ...off, hcEnabled: true, hcPath: "/health", ...patch };
    expect(healthcheckPatch(next, off)).toHaveProperty("error");
  });
});

describe("secondsUntilRestart", () => {
  it("uses the defaults when blank and the values when set", () => {
    const off = fromSvc(undefined);
    expect(secondsUntilRestart(off)).toBe(30);
    expect(secondsUntilRestart({ ...off, hcPeriod: "5", hcThreshold: "6" })).toBe(30);
    expect(secondsUntilRestart({ ...off, hcPeriod: "20" })).toBe(60);
  });
});
