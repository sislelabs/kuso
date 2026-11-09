import { describe, expect, it } from "vitest";
import type { KusoEnvironment } from "@/types/projects";
import { hasPublicIngress, stopConsequence } from "./stopCopy";

const env = (host?: string): KusoEnvironment => ({ metadata: { name: "e" }, spec: { service: "p-s", host } as KusoEnvironment["spec"] });

describe("stop copy", () => {
  it("promises a 503 only with public ingress", () => {
    expect(stopConsequence(hasPublicIngress(env("a.example.com"), false))).toMatch(/503/);
    expect(stopConsequence(hasPublicIngress(env("a.example.com"), true))).not.toMatch(/503/);
    expect(stopConsequence(hasPublicIngress(env(undefined), false))).toMatch(/background jobs/);
    expect(stopConsequence(hasPublicIngress(env("a.example.com"), false, "worker"))).toMatch(/background jobs/);
  });
});
