import { describe, expect, it } from "vitest";
import type { ProjectSummaryItem } from "@/features/projects";
import { buildPaletteIndex, envVarHref, matchScore, parseRecents, pushRecent, RECENTS_MAX, type RecentItem } from "./paletteIndex";

function item(): ProjectSummaryItem {
  return {
    project: { metadata: { name: "shop" }, spec: { description: "storefront" } },
    services: [
      {
        metadata: { name: "shop-web" },
        spec: { project: "shop", runtime: "nixpacks", repo: { url: "https://github.com/acme/shop.git" }, envVars: [{ name: "API_URL" }, { name: "" }] },
      },
      { metadata: { name: "shop-worker" }, spec: { project: "shop", runtime: "worker" } },
      {
        metadata: { name: "shop-web-staging", labels: { "kuso.sislelabs.com/env": "staging" } },
        spec: { project: "shop", runtime: "nixpacks" },
      },
    ],
    environments: [
      {
        metadata: { name: "shop-web-production", labels: { "kuso.sislelabs.com/env": "production" } },
        spec: { project: "shop", service: "shop-web", kind: "production", host: "shop.example.com" },
        status: { state: "running" },
      },
      {
        metadata: { name: "shop-worker-production", labels: { "kuso.sislelabs.com/env": "production" } },
        spec: { project: "shop", service: "shop-worker", kind: "production" },
        status: { state: "crashlooping" },
      },
      {
        metadata: { name: "shop-web-staging", labels: { "kuso.sislelabs.com/env": "staging" } },
        spec: { project: "shop", service: "shop-web-staging", kind: "production", branch: "develop" },
        status: { state: "running" },
      },
      {
        metadata: { name: "shop-web-pr-4", labels: { "kuso.sislelabs.com/env": "preview-pr-4" } },
        spec: { project: "shop", service: "shop-web", kind: "preview" },
      },
    ],
    metrics: { project: "shop", cpuMillicores: 0, memBytes: 0, pods: 0, envs: 1 },
  } as ProjectSummaryItem;
}

describe("buildPaletteIndex", () => {
  const idx = buildPaletteIndex([item()]);

  it("uses short service names and routes env-group clones to their group", () => {
    const web = idx.services.find((s) => s.name === "web");
    const staging = idx.services.find((s) => s.name === "web-staging");
    expect(web?.query).toBe("service=web");
    expect(web?.repo).toBe("acme/shop");
    expect(web?.url).toBe("https://shop.example.com");
    expect(web?.envVarKeys).toEqual(["API_URL"]);
    expect(staging?.display).toBe("web (staging)");
    expect(staging?.query).toBe("service=web-staging&env=staging");
    expect(staging?.branch).toBe("develop");
  });

  it("rolls the project up worst-first from production services only", () => {
    expect(idx.projects).toEqual([
      { name: "shop", description: "storefront", services: 2, live: 1, status: "failing" },
    ]);
  });

  it("lists non-production env groups once each with their kind", () => {
    expect(idx.envs).toEqual([
      { project: "shop", group: "staging", kind: "custom", services: 1 },
      { project: "shop", group: "preview-pr-4", kind: "preview", services: 1 },
    ]);
  });

  it("builds the variables highlight deep link", () => {
    const web = idx.services.find((s) => s.name === "web")!;
    expect(envVarHref(web, "API_URL")).toBe("/projects/shop?service=web&tab=variables&highlight=API_URL");
  });
});

describe("recents", () => {
  it("dedupes to the front and caps the list", () => {
    let list: RecentItem[] = [];
    for (let i = 0; i < RECENTS_MAX + 3; i++) list = pushRecent(list, { kind: "project", project: `p${i}` });
    list = pushRecent(list, { kind: "project", project: "p5" });
    expect(list).toHaveLength(RECENTS_MAX);
    expect(list[0]).toEqual({ kind: "project", project: "p5" });
    expect(list.filter((r) => r.project === "p5")).toHaveLength(1);
  });

  it("drops malformed stored entries instead of throwing", () => {
    expect(parseRecents("not json")).toEqual([]);
    expect(parseRecents(JSON.stringify([{ kind: "service", project: "a" }, { kind: "project", project: "b" }]))).toEqual([
      { kind: "project", project: "b" },
    ]);
  });
});

describe("matchScore", () => {
  it("ranks exact and prefix label hits above keyword-only hits", () => {
    const exact = matchScore("web", "web");
    const prefix = matchScore("web", "web-staging");
    const word = matchScore("staging", "web-staging");
    const kw = matchScore("cron", "Add service", ["cron"]);
    expect(exact).toBeGreaterThan(prefix);
    expect(prefix).toBeGreaterThan(word);
    expect(word).toBeGreaterThan(kw);
    expect(kw).toBeGreaterThan(0);
  });

  it("requires every word to match and keeps short queries strict", () => {
    expect(matchScore("tickero api", "api", ["tickero"])).toBeGreaterThan(0);
    expect(matchScore("tickero web", "api", ["tickero"])).toBe(0);
    expect(matchScore("web", "web")).toBeGreaterThan(matchScore("web", "web-staging"));
    // Context words never fuzz: l…o…g…s in "operator-staging · puncho".
    expect(matchScore("logs", "OPERATOR_MODEL", ["operator-staging", "puncho"])).toBe(0);
    // "wr" is a letter-subsequence of "worker", but too short to fuzz.
    expect(matchScore("wr", "worker")).toBe(0);
    expect(matchScore("wkr", "worker")).toBeGreaterThan(0);
    // Keywords never fuzz: "new schedule job" contains w…e…b in order.
    expect(matchScore("web", "Add cron job", ["new", "schedule", "job"])).toBe(0);
    expect(matchScore("bckoffice", "backoffice")).toBeGreaterThan(0);
  });
});
