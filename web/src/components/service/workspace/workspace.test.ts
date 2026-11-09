import { describe, expect, it } from "vitest";
import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import { addonPageHref, resourceFromPath, servicePageHref } from "./hrefs";
import { railResources } from "./railResources";

const svc = (name: string, env?: string) =>
  ({ metadata: { name, labels: env ? { "kuso.sislelabs.com/env": env } : {} }, spec: {} }) as unknown as KusoService;
const addon = (name: string, env?: string) =>
  ({ metadata: { name, labels: env ? { "kuso.sislelabs.com/env": env } : {} }, spec: { kind: "postgres" } }) as unknown as KusoAddon;

describe("full-page hrefs", () => {
  it("leaves production out and keeps the rest", () => {
    expect(servicePageHref("shop", "web")).toBe("/projects/shop/services/web");
    expect(servicePageHref("shop", "web", { env: "staging", tab: "logs" })).toBe("/projects/shop/services/web?env=staging&tab=logs");
    expect(addonPageHref("shop", "shop-db", { tab: "sql" })).toBe("/projects/shop/addons/db?tab=sql");
  });

  it("reads resources positionally, so names like settings survive", () => {
    expect(resourceFromPath("/projects/shop/services/settings")).toEqual({ project: "shop", kind: "service", name: "settings" });
    expect(resourceFromPath("/projects/shop/addons/db/")).toEqual({ project: "shop", kind: "addon", name: "db" });
    expect(resourceFromPath("/projects/_/services/_")).toBeNull();
    expect(resourceFromPath("/projects/shop")).toBeNull();
  });
});

describe("railResources", () => {
  const services = [svc("shop-web"), svc("shop-api"), svc("shop-api-staging", "staging")];
  const addons = [addon("shop-db"), addon("shop-db-staging", "staging"), addon("shop-cache")];
  const envs: KusoEnvironment[] = [];

  it("shows shared resources on production, not clones", () => {
    const r = railResources("shop", "production", services, envs, addons);
    expect(r.services.map((s) => s.name)).toEqual(["api", "web"]);
    expect(r.addons.map((a) => a.name)).toEqual(["cache", "db"]);
  });

  it("swaps a shared base for this env's clone", () => {
    const r = railResources("shop", "staging", services, envs, addons);
    expect(r.services.map((s) => s.name)).toEqual(["api-staging", "web"]);
    expect(r.addons.map((a) => a.name)).toEqual(["cache", "db-staging"]);
  });
});
