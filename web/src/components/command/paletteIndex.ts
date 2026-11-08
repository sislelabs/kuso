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
