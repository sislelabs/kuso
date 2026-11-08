import { describe, expect, it } from "vitest";
import type { MarketplaceApp, MarketplacePrompt } from "./api";
import { answersPayload, landingPath, promptError, splitPrompts, uniqueProjectName } from "./deploy";

const host: MarketplacePrompt = { key: "host", title: "Domain", kind: "domain", required: true };
const user: MarketplacePrompt = { key: "admin_user", title: "Admin user", kind: "string", default: "admin" };
const app: MarketplaceApp = { name: "kuma", title: "Kuma", description: "", category: "monitoring", prompts: [host, user] };

describe("promptError", () => {
  it("requires required answers", () => {
    expect(promptError(host, " ")).toMatch(/required/);
  });
  it("rejects URLs and bare words for domains", () => {
    expect(promptError(host, "https://status.example.com")).toMatch(/without http/);
    expect(promptError(host, "status.example.com/x")).toMatch(/path/);
    expect(promptError(host, "localhost")).toMatch(/valid hostname/);
    expect(promptError(host, "Status.Example.com")).toBeNull();
  });
});

describe("splitPrompts", () => {
  it("keeps pre-filled prompts out of the up-front section", () => {
    const { required, advanced } = splitPrompts(app);
    expect(required.map((p) => p.key)).toEqual(["host"]);
    expect(advanced.map((p) => p.key)).toEqual(["admin_user"]);
  });
});

describe("answersPayload", () => {
  it("drops empty answers and lowercases domains", () => {
    const vals: Record<string, string> = { host: "Status.Example.com", admin_user: "" };
    expect(answersPayload(app, (p) => vals[p.key] ?? "")).toEqual({ host: "status.example.com" });
  });
});

describe("landingPath", () => {
  const empty = { servicesToCreate: [], servicesToUpdate: [], addonsToCreate: [], addonsToUpdate: [] };
  it("opens the created service on deployments", () => {
    expect(landingPath("p", { ...empty, servicesToCreate: ["web"], addonsToCreate: ["db"] })).toBe(
      "/projects/p?service=web&tab=deployments",
    );
  });
  it("falls back to the addon for addon-only apps", () => {
    expect(landingPath("p", { ...empty, addonsToCreate: ["db"] })).toBe("/projects/p?addon=db");
  });
});

describe("uniqueProjectName", () => {
  it("suffixes taken names", () => {
    expect(uniqueProjectName("kuma", new Set(["kuma", "kuma-2"]))).toBe("kuma-3");
    expect(uniqueProjectName("kuma", new Set())).toBe("kuma");
  });
});
