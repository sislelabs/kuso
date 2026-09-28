import { describe, expect, it } from "vitest";
import type { KusoEnvironment } from "@/types/projects";
import { envGroupName, isProductionGroup } from "./env-group";

function env(kind: string, label?: string): KusoEnvironment {
  return {
    metadata: { name: "x", labels: label ? { "kuso.sislelabs.com/env": label } : {} },
    spec: { kind },
  } as unknown as KusoEnvironment;
}

describe("envGroupName", () => {
  it("names a staging clone by its group, not its production kind", () => {
    const staging = env("production", "staging");
    expect(envGroupName(staging)).toBe("staging");
    expect(isProductionGroup(staging)).toBe(false);
  });

  it("returns production for the production group", () => {
    expect(envGroupName(env("production", "production"))).toBe("production");
  });

  it("falls back to spec.kind for legacy envs without the label", () => {
    expect(envGroupName(env("preview"))).toBe("preview");
  });

  it("defaults to production when no env is loaded", () => {
    expect(envGroupName(undefined)).toBe("production");
  });
});
