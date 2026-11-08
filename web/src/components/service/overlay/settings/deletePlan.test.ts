import { describe, expect, it } from "vitest";
import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import { exclusiveAddons, serviceEnvs } from "./deletePlan";

const svc = (name: string, spec: Partial<KusoService["spec"]>) =>
  ({ metadata: { name: `p-${name}` }, spec: { project: "p", ...spec } }) as unknown as KusoService;
const addon = (name: string) => ({ metadata: { name: `p-${name}` }, spec: { kind: "postgres" } }) as unknown as KusoAddon;

describe("exclusiveAddons", () => {
  const addons = [addon("db"), addon("cache"), addon("queue")];
  it("returns addons only the deleted service uses", () => {
    const services = [
      svc("api", { subscribedAddons: ["db", "cache"] }),
      svc("web", { subscribedAddons: ["cache"] }),
      svc("worker", { subscribedAddons: [] }),
    ];
    expect(exclusiveAddons("p", "api", services, addons).map((a) => a.metadata.name)).toEqual(["p-db"]);
  });
  it("treats an unset subscription as using every addon", () => {
    const services = [svc("api", { subscribedAddons: ["db"] }), svc("web", {})];
    expect(exclusiveAddons("p", "api", services, addons)).toEqual([]);
  });
  it("counts another service's env-var reference as a use", () => {
    const services = [
      svc("api", { subscribedAddons: ["queue"] }),
      svc("web", {
        subscribedAddons: [],
        envVars: [{ name: "Q", valueFrom: { secretKeyRef: { name: "p-queue-conn", key: "URL" } } }],
      }),
    ];
    expect(exclusiveAddons("p", "api", services, addons)).toEqual([]);
  });
});

describe("serviceEnvs", () => {
  it("lists production first, then previews and named envs", () => {
    const env = (name: string, kind: KusoEnvironment["spec"]["kind"], extra: object = {}) =>
      ({ metadata: { name, labels: {} }, spec: { project: "p", service: "p-api", kind, ...extra } }) as unknown as KusoEnvironment;
    const out = serviceEnvs("p", "api", [
      env("p-api-pr-3", "preview", { pullRequest: { number: 3 } }),
      env("p-api-production", "production"),
      { ...env("p-web-production", "production"), spec: { project: "p", service: "p-web", kind: "production" } } as KusoEnvironment,
    ]);
    expect(out.map((e) => e.label)).toEqual(["production", "PR #3"]);
  });
});
