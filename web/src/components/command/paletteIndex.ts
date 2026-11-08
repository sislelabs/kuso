import type { ProjectSummaryItem } from "@/features/projects";
import { ENV_GROUP_LABEL, envGroupName } from "@/lib/env-group";
import { serviceShortName } from "@/lib/utils";

// One status vocabulary for every palette row, folded from the
// server's unified env state (projects/state.go).
export type PaletteStatus = "live" | "deploying" | "failing" | "sleeping" | "stopped" | "idle";

export function paletteStatus(state: string | undefined): PaletteStatus {
  switch (state) {
    case "running":
      return "live";
    case "building":
    case "deploying":
      return "deploying";
    case "degraded":
    case "crashlooping":
    case "build_failed":
    case "release_failed":
      return "failing";
    case "sleeping":
      return "sleeping";
    case "stopped":
      return "stopped";
    default:
      return "idle";
  }
}

export interface PaletteService {
  project: string;
  // Short name ("web"), what ?service= and the service API take.
  name: string;
  // Env-group this CR belongs to; clones carry their group label.
  group: string;
  display: string;
  // Query string that opens this service's overlay on the canvas.
  query: string;
  branch?: string;
  runtime?: string;
  repo?: string;
  url?: string;
  status: PaletteStatus;
  envVarKeys: string[];
}

export interface PaletteProject {
  name: string;
  description?: string;
  services: number;
  live: number;
  status: PaletteStatus;
}

export interface PaletteEnv {
  project: string;
  group: string;
  kind: "preview" | "custom";
  services: number;
}

export interface PaletteIndex {
  projects: PaletteProject[];
  services: PaletteService[];
  envs: PaletteEnv[];
}

// Worst-first, so one failing service marks the whole project.
const STATUS_RANK: PaletteStatus[] = ["failing", "deploying", "live", "sleeping", "stopped", "idle"];

function rollup(statuses: PaletteStatus[]): PaletteStatus {
  for (const s of STATUS_RANK) if (statuses.includes(s)) return s;
  return "idle";
}

// buildPaletteIndex flattens the batched /api/projects/summary payload
// into searchable rows: one request covers every project, service and
// env-group the caller can see, so the palette never fans out per project.
export function buildPaletteIndex(items: ProjectSummaryItem[]): PaletteIndex {
  const projects: PaletteProject[] = [];
  const services: PaletteService[] = [];
  const envs: PaletteEnv[] = [];

  for (const item of items) {
    const project = item.project?.metadata?.name;
    if (!project) continue;
    const envList = item.environments ?? [];
    const projectServices: PaletteService[] = [];

    for (const s of item.services ?? []) {
      const name = serviceShortName(project, s.metadata.name);
      const label = s.metadata.labels?.[ENV_GROUP_LABEL];
      const group = label && label !== "production" ? label : "production";
      const base = group !== "production" && name.endsWith(`-${group}`) ? name.slice(0, -(group.length + 1)) : name;
      const env = envList.find((e) => e.spec.service === s.metadata.name && envGroupName(e) === group);
      let shown = s.spec.displayName?.trim() || base;
      if (group !== "production" && shown.endsWith(`-${group}`)) shown = shown.slice(0, -(group.length + 1));
      projectServices.push({
        project,
        name,
        group,
        display: group === "production" ? shown : `${shown} (${group})`,
        query: group === "production" ? `service=${encodeURIComponent(name)}` : `service=${encodeURIComponent(name)}&env=${encodeURIComponent(group)}`,
        branch: group === "production" ? undefined : env?.spec.branch,
        runtime: s.spec.runtime,
        repo: s.spec.repo?.url?.replace(/^https?:\/\/(www\.)?github\.com\//, "").replace(/\.git$/, ""),
        url: env?.status?.url || (env?.spec.host ? `${env.spec.tlsEnabled === false ? "http" : "https"}://${env.spec.host}` : undefined),
        status: paletteStatus(env?.status?.state),
        envVarKeys: (s.spec.envVars ?? []).map((v) => v.name).filter((k): k is string => !!k),
      });
    }
    projectServices.sort((a, b) => a.display.localeCompare(b.display));
    services.push(...projectServices);

    const prod = projectServices.filter((s) => s.group === "production");
    projects.push({
      name: project,
      description: item.project.spec?.description,
      services: prod.length,
      live: prod.filter((s) => s.status === "live").length,
      status: rollup(prod.map((s) => s.status)),
    });

    const groups = new Map<string, PaletteEnv>();
    for (const e of envList) {
      const group = envGroupName(e);
      if (group === "production") continue;
      const row = groups.get(group) ?? {
        project,
        group,
        kind: e.spec.kind === "preview" ? "preview" : "custom",
        services: 0,
      };
      row.services++;
      groups.set(group, row);
    }
    envs.push(...groups.values());
  }
  projects.sort((a, b) => a.name.localeCompare(b.name));
  return { projects, services, envs };
}

// Recents: what the user last jumped to, newest first. Kept small and
// local — it's a per-browser convenience, not state anyone else needs.
export type RecentItem =
  | { kind: "project"; project: string }
  | { kind: "service"; project: string; name: string; group: string }
  | { kind: "addon"; project: string; name: string };

export const RECENTS_KEY = "kuso.palette.recents";
export const RECENTS_MAX = 8;

export function recentKey(r: RecentItem): string {
  return r.kind === "project" ? `p:${r.project}` : r.kind === "service" ? `s:${r.project}/${r.name}@${r.group}` : `a:${r.project}/${r.name}`;
}

export function pushRecent(list: RecentItem[], item: RecentItem): RecentItem[] {
  const key = recentKey(item);
  return [item, ...list.filter((r) => recentKey(r) !== key)].slice(0, RECENTS_MAX);
}

function isRecent(v: unknown): v is RecentItem {
  if (!v || typeof v !== "object") return false;
  const o = v as Record<string, unknown>;
  if (typeof o.project !== "string" || !o.project) return false;
  if (o.kind === "project") return true;
  if (o.kind === "addon") return typeof o.name === "string";
  if (o.kind === "service") return typeof o.name === "string" && typeof o.group === "string";
  return false;
}

export function parseRecents(raw: string | null): RecentItem[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter(isRecent).slice(0, RECENTS_MAX) : [];
  } catch {
    return [];
  }
}

export function loadRecents(): RecentItem[] {
  try {
    return parseRecents(window.localStorage.getItem(RECENTS_KEY));
  } catch {
    return [];
  }
}

export function saveRecents(list: RecentItem[]): void {
  try {
    window.localStorage.setItem(RECENTS_KEY, JSON.stringify(list));
  } catch {
    // Private mode / blocked storage: recents just don't persist.
  }
}

// Deep link to a service's Variables tab with one key highlighted.
// Contract with the Variables panel (S5): `highlight=<KEY>` scrolls to
// and flashes that row.
export function envVarHref(s: Pick<PaletteService, "project" | "query">, key: string): string {
  return `/projects/${encodeURIComponent(s.project)}?${s.query}&tab=variables&highlight=${encodeURIComponent(key)}`;
}

export function serviceHref(s: Pick<PaletteService, "project" | "query">, tab?: string): string {
  return `/projects/${encodeURIComponent(s.project)}?${s.query}${tab ? `&tab=${tab}` : ""}`;
}

function isSubsequence(needle: string, hay: string): boolean {
  let i = 0;
  for (const ch of hay) if (ch === needle[i]) i++;
  return i === needle.length;
}

// matchScore ranks a row for a query: 0 hides it. label is the row's
// own name; keywords carry context (project, runtime, synonyms). Every
// query word must hit one of them; label hits beat keyword hits and
// word-prefix beats substring. A loose in-order letter match is allowed
// on the label only, for 3+ char words sharing the first letter — it
// catches typos like "bckoffice" without "logs" matching every label
// that happens to contain l…o…g…s.
export function matchScore(query: string, label: string, keywords: string[] = []): number {
  const q = query.trim().toLowerCase();
  if (!q) return 1;
  const l = label.toLowerCase();
  // Tie-break toward shorter labels so "web" beats "web-staging".
  const brevity = Math.min(l.length, 60) * 0.001;
  if (l === q) return 1;
  if (l.startsWith(q)) return 0.95 - brevity;
  const split = (s: string) => s.split(/[\s\-_./·():]+/).filter(Boolean);
  const labelWords = split(l);
  const kw = keywords.map((k) => k.toLowerCase());
  const kwWords = kw.flatMap(split);
  const compact = l.replace(/[\s\-_.]+/g, "");
  let total = 0;
  const tokens = q.split(/\s+/);
  for (const t of tokens) {
    if (labelWords.some((w) => w === t)) total += 0.9;
    else if (labelWords.some((w) => w.startsWith(t))) total += 0.8;
    else if (l.includes(t)) total += 0.65;
    else if (kwWords.some((w) => w.startsWith(t))) total += 0.5;
    else if (kw.some((k) => k.includes(t))) total += 0.35;
    else if (t.length >= 3 && compact[0] === t[0] && isSubsequence(t, compact)) total += 0.2;
    else return 0;
  }
  return (total / tokens.length) * 0.9 - brevity;
}

// Global service actions: "Logs: shop/web", "Redeploy: shop/web", … for
// every service in every project, so a verb typed anywhere finds its
// target without drilling into a project first.
export type ServiceActionKind = "logs" | "deployments" | "variables" | "metrics" | "settings" | "redeploy" | "restart";

export interface ServiceAction {
  kind: ServiceActionKind;
  verb: string;
  label: string;
  keywords: string[];
  svc: PaletteService;
  // Overlay tab to open; absent for the confirm-first actions.
  tab?: string;
}

const ACTION_DEFS: { kind: ServiceActionKind; verb: string; tab?: string; keywords: string[] }[] = [
  { kind: "logs", verb: "Logs", tab: "logs", keywords: ["tail", "output", "stdout"] },
  { kind: "deployments", verb: "Deployments", tab: "deployments", keywords: ["builds", "history", "rollback"] },
  { kind: "variables", verb: "Variables", tab: "variables", keywords: ["env", "secrets", "config"] },
  { kind: "metrics", verb: "Metrics", tab: "metrics", keywords: ["cpu", "memory", "latency", "requests"] },
  { kind: "settings", verb: "Settings", tab: "settings", keywords: ["config", "domain", "scale", "port"] },
  { kind: "redeploy", verb: "Redeploy", keywords: ["deploy", "build", "trigger", "latest"] },
  { kind: "restart", verb: "Restart", keywords: ["reboot", "roll", "pods"] },
];

// Image and worker services have no repo build to trigger.
export function canRedeploy(s: Pick<PaletteService, "runtime">): boolean {
  return s.runtime !== "image" && s.runtime !== "worker";
}

export function serviceActions(services: PaletteService[]): ServiceAction[] {
  const out: ServiceAction[] = [];
  for (const def of ACTION_DEFS) {
    for (const svc of services) {
      if (def.kind === "redeploy" && !canRedeploy(svc)) continue;
      out.push({
        kind: def.kind,
        verb: def.verb,
        label: `${def.verb}: ${pathLabel(svc.project, svc.display)}`,
        keywords: [...def.keywords, svc.name, svc.project, svc.group],
        svc,
        tab: def.tab,
      });
    }
  }
  return out;
}

// The one way the palette names something inside a project.
export function pathLabel(project: string, name: string): string {
  return `${project} / ${name}`;
}

// Ranking tiers. Each row's match score gets a boost by how close the
// row is to what the user is doing: the project in the URL, then what
// they opened recently, then their starred projects. The gaps (0.3 /
// ≤0.22 / 0.1) are smaller than the gap between a label hit and a
// keyword-only hit, so tiers reorder comparable matches without letting
// a weak fuzzy hit in the current project jump an exact hit elsewhere.
export const TIER_BOOST = { current: 0.3, recent: 0.2, starred: 0.1 } as const;
const RECENT_DECAY = 0.01;
const OWN_RECENT_NUDGE = 0.02;

export interface RankContext {
  currentProject: string;
  recents: RecentItem[];
  starred: ReadonlySet<string>;
}

// key is the row's own recentKey when it is a recents-able entity.
export function tierBoost(ctx: RankContext, ref: { project: string; key?: string }): number {
  const own = ref.key !== undefined && ctx.recents.some((r) => recentKey(r) === ref.key) ? OWN_RECENT_NUDGE : 0;
  if (ctx.currentProject && ref.project === ctx.currentProject) return TIER_BOOST.current + own;
  const i = ctx.recents.findIndex((r) => r.project === ref.project);
  if (i >= 0) return TIER_BOOST.recent - RECENT_DECAY * Math.min(i, RECENTS_MAX - 1) + own;
  if (ctx.starred.has(ref.project)) return TIER_BOOST.starred;
  return 0;
}

export function rankScore(match: number, boost: number): number {
  return match > 0 ? match + boost : 0;
}

// Past this many service hits spread over several projects, a flat list
// stops being scannable, so the palette splits it into one group per
// project.
export const GROUP_BY_PROJECT_MIN = 6;

export function shouldGroupByProject(hits: Pick<PaletteService, "project">[]): boolean {
  return hits.length >= GROUP_BY_PROJECT_MIN && new Set(hits.map((h) => h.project)).size > 1;
}

// Last index the palette built, so reopening (or a fresh tab) renders
// results instantly while the summary refetches in the background.
export const INDEX_SNAPSHOT_KEY = "kuso.palette.index.v1";

function isObj(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

const STATUSES: readonly string[] = STATUS_RANK;

function isStatus(v: unknown): v is PaletteStatus {
  return typeof v === "string" && STATUSES.includes(v);
}

function isSnapService(v: unknown): v is PaletteService {
  return (
    isObj(v) &&
    typeof v.project === "string" &&
    typeof v.name === "string" &&
    typeof v.group === "string" &&
    typeof v.display === "string" &&
    typeof v.query === "string" &&
    isStatus(v.status) &&
    Array.isArray(v.envVarKeys) &&
    v.envVarKeys.every((k) => typeof k === "string")
  );
}

function isSnapProject(v: unknown): v is PaletteProject {
  return isObj(v) && typeof v.name === "string" && typeof v.services === "number" && typeof v.live === "number" && isStatus(v.status);
}

function isSnapEnv(v: unknown): v is PaletteEnv {
  return (
    isObj(v) &&
    typeof v.project === "string" &&
    typeof v.group === "string" &&
    (v.kind === "preview" || v.kind === "custom") &&
    typeof v.services === "number"
  );
}

// Keyed to the user, so a shared browser never shows one account's
// projects to the next before the refetch lands.
export function parseIndexSnapshot(raw: string | null, user: string): PaletteIndex | null {
  if (!raw || !user) return null;
  try {
    const o: unknown = JSON.parse(raw);
    if (!isObj(o) || o.user !== user) return null;
    const v = o.index;
    if (!isObj(v) || !Array.isArray(v.projects) || !Array.isArray(v.services) || !Array.isArray(v.envs)) return null;
    return {
      projects: v.projects.filter(isSnapProject),
      services: v.services.filter(isSnapService),
      envs: v.envs.filter(isSnapEnv),
    };
  } catch {
    return null;
  }
}

export function loadIndexSnapshot(user: string): PaletteIndex | null {
  try {
    return parseIndexSnapshot(window.localStorage.getItem(INDEX_SNAPSHOT_KEY), user);
  } catch {
    return null;
  }
}

export function saveIndexSnapshot(index: PaletteIndex, user: string): void {
  if (!user) return;
  try {
    window.localStorage.setItem(INDEX_SNAPSHOT_KEY, JSON.stringify({ user, index }));
  } catch {
    // Quota / blocked storage: the palette just waits for the fetch.
  }
}
