import { describe, expect, it } from "vitest";
import type { ProjectSummaryItem } from "@/features/projects";
import {
  buildPaletteIndex,
  envVarHref,
  matchScore,
  parseIndexSnapshot,
  parseRecents,
  pushRecent,
  rankScore,
  RECENTS_MAX,
  serviceActions,
  shouldGroupByProject,
  tierBoost,
  type PaletteService,
  type RankContext,
  type RecentItem,
} from "./paletteIndex";

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

describe("serviceActions", () => {
  const idx = buildPaletteIndex([item()]);
  const actions = serviceActions(idx.services);

  it("offers every verb for every service, skipping redeploy where there's no build", () => {
    const labels = actions.map((a) => a.label);
    expect(labels).toContain("Logs: shop / web");
    expect(labels).toContain("Restart: shop / web (staging)");
    expect(labels).toContain("Redeploy: shop / web");
    expect(labels).not.toContain("Redeploy: shop / worker");
  });

  it("matches a bare verb, so typing logs anywhere finds every service's logs", () => {
    const hits = actions.filter((a) => matchScore("logs", a.label, a.keywords) > 0).map((a) => a.label);
    expect(hits).toEqual(["Logs: shop / web", "Logs: shop / web (staging)", "Logs: shop / worker"]);
    const scoped = actions.filter((a) => matchScore("logs worker", a.label, a.keywords) > 0);
    expect(scoped.map((a) => a.label)).toEqual(["Logs: shop / worker"]);
  });
});

function svc(project: string, name: string): PaletteService {
  return { project, name, group: "production", display: name, query: `service=${name}`, runtime: "nixpacks", status: "live", envVarKeys: [] };
}

function rank(services: PaletteService[], query: string, ctx: RankContext): string[] {
  return serviceActions(services)
    .map((a) => ({
      label: a.label,
      score: rankScore(
        matchScore(query, a.label, a.keywords),
        tierBoost(ctx, { project: a.svc.project, key: `s:${a.svc.project}/${a.svc.name}@${a.svc.group}` }),
      ),
    }))
    .filter((r) => r.score > 0)
    .sort((a, b) => b.score - a.score)
    .map((r) => r.label);
}

describe("tier ranking", () => {
  // Long project names first, so brevity alone would rank them last.
  const services = [
    svc("a-very-long-project-name", "api"),
    svc("starred-proj", "api"),
    svc("recent-proj", "api"),
    svc("tickero", "backoffice-api"),
    svc("zz", "api"),
  ];
  const ctx: RankContext = {
    currentProject: "tickero",
    recents: [{ kind: "project", project: "recent-proj" }],
    starred: new Set(["starred-proj"]),
  };

  it("orders redeploy targets current project > recent > starred > rest", () => {
    expect(rank(services, "redeploy", ctx)).toEqual([
      "Redeploy: tickero / backoffice-api",
      "Redeploy: recent-proj / api",
      "Redeploy: starred-proj / api",
      "Redeploy: zz / api",
      "Redeploy: a-very-long-project-name / api",
    ]);
  });

  it("lets a much better match beat a tier", () => {
    // Exact project-name hit elsewhere vs a keyword-only hit in the current project.
    const out = rank([svc("tickero", "web"), svc("shop", "web")], "logs shop", { ...ctx, recents: [] });
    expect(out[0]).toBe("Logs: shop / web");
    const loose = matchScore("tail", "Logs: tickero / web", ["tail"]);
    expect(loose + tierBoost(ctx, { project: "tickero" })).toBeLessThan(matchScore("logs", "Logs: zz / web"));
  });

  it("favours the more recent project and the row's own recent entry", () => {
    const c: RankContext = {
      currentProject: "",
      recents: [
        { kind: "service", project: "b", name: "web", group: "production" },
        { kind: "project", project: "a" },
      ],
      starred: new Set(["a", "b", "c"]),
    };
    const b = tierBoost(c, { project: "b", key: "s:b/web@production" });
    const bOther = tierBoost(c, { project: "b", key: "s:b/api@production" });
    const a = tierBoost(c, { project: "a" });
    const starredOnly = tierBoost(c, { project: "c" });
    expect(b).toBeGreaterThan(bOther);
    expect(bOther).toBeGreaterThan(a);
    expect(a).toBeGreaterThan(starredOnly);
    expect(starredOnly).toBeGreaterThan(tierBoost(c, { project: "d" }));
    expect(rankScore(0, 1)).toBe(0);
  });
});

describe("shouldGroupByProject", () => {
  it("groups only many hits spread over several projects", () => {
    const many = ["a", "b", "c", "d", "e", "f"].map((n, i) => svc(i < 3 ? "p1" : "p2", n));
    expect(shouldGroupByProject(many)).toBe(true);
    expect(shouldGroupByProject(many.slice(0, 5))).toBe(false);
    expect(shouldGroupByProject(many.map((s) => ({ ...s, project: "p1" })))).toBe(false);
  });
});

describe("parseIndexSnapshot", () => {
  const snap = (index: unknown, user = "u1") => JSON.stringify({ user, index });

  it("round-trips a built index for the same user only", () => {
    const idx = buildPaletteIndex([item()]);
    expect(parseIndexSnapshot(snap(idx), "u1")).toEqual(JSON.parse(JSON.stringify(idx)));
    expect(parseIndexSnapshot(snap(idx), "u2")).toBeNull();
    expect(parseIndexSnapshot(snap(idx), "")).toBeNull();
  });

  it("rejects junk and drops malformed rows", () => {
    expect(parseIndexSnapshot("nope", "u1")).toBeNull();
    expect(parseIndexSnapshot(snap({ projects: [] }), "u1")).toBeNull();
    expect(
      parseIndexSnapshot(snap({ projects: [{ name: "x" }], services: [{ ...svc("p", "w"), status: "weird" }], envs: [] }), "u1"),
    ).toEqual({ projects: [], services: [], envs: [] });
  });
});
