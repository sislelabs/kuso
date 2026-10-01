import { describe, expect, it } from "vitest";
import type { KusoEnvironment } from "@/types/projects";
import { envGroupName, isProductionGroup, serviceEnvOptions, wakeEnvParam } from "./env-group";

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

function named(name: string, service: string, kind: string, label?: string): KusoEnvironment {
  return {
    metadata: { name, labels: label ? { "kuso.sislelabs.com/env": label } : {} },
    spec: { kind, service },
  } as unknown as KusoEnvironment;
}

describe("wakeEnvParam", () => {
  it("omits env for production and sends the CR name otherwise", () => {
    expect(wakeEnvParam(named("p-api-production", "p-api", "production", "production"), "production")).toBeUndefined();
    // A preview CR is "<fqn>-pr-N"; the group name would not resolve.
    expect(wakeEnvParam(named("p-api-pr-7", "p-api", "preview", "preview-pr-7"), "preview-pr-7")).toBe("p-api-pr-7");
    expect(wakeEnvParam(undefined, "staging")).toBe("staging");
    expect(wakeEnvParam(undefined, "production")).toBeUndefined();
  });
});

describe("serviceEnvOptions", () => {
  it("lists only this service's envs, production first", () => {
    const opts = serviceEnvOptions(
      [
        named("p-api-staging", "p-api", "production", "staging"),
        named("p-web-production", "p-web", "production", "production"),
        named("p-api-production", "p-api", "production", "production"),
      ],
      "p-api",
    );
    expect(opts.map((o) => o.value)).toEqual(["production", "staging"]);
  });
});
