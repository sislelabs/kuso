import { stripRepoCredentials } from "@/lib/format";
import { ENV_GROUP_LABEL, isProductionGroup } from "@/lib/env-group";
import { projectLastDeployedAt, projectPrimaryDomain } from "@/features/projects/facts";
import type { KusoEnvironment, KusoProject, KusoService } from "@/types/projects";
import type { ProjectSummaryItem, ProjectSummaryMetrics } from "@/features/projects";

// One health answer per project card. "pending" = services exist but none
// has ever produced a running image; "empty" = no services yet.
export type ProjectStatus =
  | "live"
  | "failing"
  | "building"
  | "sleeping"
  | "stopped"
  | "pending"
  | "empty";

export type ServiceHealth = Exclude<ProjectStatus, "empty">;

const SERVICE_LABEL = "kuso.sislelabs.com/service";

export interface ProjectCardModel {
  name: string;
  description?: string;
  createdAt?: string;
  status: ProjectStatus;
  // Production services (env-group clones excluded) and how many are
  // serving / intentionally stopped. readyOf counts the non-stopped ones.
  serviceCount: number;
  liveCount: number;
  stoppedCount: number;
  readyOf: number;
  failingCount: number;
  // stateDetail of the first failing/building service, for the tooltip.
  detail?: string;
  // Latest promotion (build → live) across production envs. Undefined
  // when nothing has deployed yet.
  lastDeployAt?: string;
  domain?: string;
  domainHref?: string;
  repo?: string;
  repoHref?: string;
  publicURL?: string;
  serviceNames: string[];
  metrics?: ProjectSummaryMetrics;
  // Every service incl. env-group clones — project stop/start acts on all.
  allServiceCount: number;
  // False until the batched summary has loaded (or when it failed); the
  // card then shows no health rather than a guessed one.
  hasSummary: boolean;
}

// classifyEnv folds the server's unified per-env state into the card's
// vocabulary. A missing state (older servers) falls back to the replica
// count / ready flag the cards used before state existed.
export function classifyEnv(svc: KusoService, env: KusoEnvironment | undefined): ServiceHealth {
  if (svc.spec.stopped === true) return "stopped";
  if (!env) return "pending";
  const st = env.status;
  switch (st?.state) {
    case "running":
      return "live";
    case "degraded":
    case "crashlooping":
    case "build_failed":
    case "release_failed":
      return "failing";
    case "building":
    case "deploying":
      return "building";
    case "sleeping":
      return "sleeping";
    case "stopped":
      return "stopped";
    case "no_image":
      return "pending";
  }
  const replicas = st?.replicas as { ready?: number } | undefined;
  if ((replicas?.ready ?? 0) > 0 || st?.ready === true) return "live";
  return "pending";
}

export function rollupStatus(health: ReadonlyArray<ServiceHealth>): ProjectStatus {
  if (health.length === 0) return "empty";
  const has = (h: ServiceHealth) => health.includes(h);
  if (has("failing")) return "failing";
  if (has("building")) return "building";
  if (has("live")) return "live";
  if (has("sleeping")) return "sleeping";
  if (health.every((h) => h === "stopped")) return "stopped";
  return "pending";
}

function prodEnvFor(svc: KusoService, envs: ReadonlyArray<KusoEnvironment>): KusoEnvironment | undefined {
  return envs.find((e) => e.spec.service === svc.metadata.name && isProductionGroup(e));
}

export function serviceShortName(s: KusoService): string {
  return s.metadata.labels?.[SERVICE_LABEL] ?? s.metadata.name;
}

// The single repo URL shared by all services that declare one; undefined
// for multi-repo projects (the card shows none rather than guessing).
export function distinctServiceRepo(services: ReadonlyArray<KusoService>): string | undefined {
  const urls = new Set<string>();
  for (const s of services) {
    const u = s.spec.repo?.url;
    if (u) urls.add(u);
  }
  return urls.size === 1 ? [...urls][0] : undefined;
}

// repoWebURL turns a stored clone URL (https, scp-style ssh, or bare
// host/path, with or without .git) into a browser link, or null when it
// can't form a confident one.
export function repoWebURL(raw?: string): string | null {
  if (!raw) return null;
  let s = raw.trim();
  if (!s) return null;
  const scp = s.match(/^[\w.-]+@([\w.-]+):(.+)$/);
  if (scp) {
    s = `${scp[1]}/${scp[2]}`;
  } else {
    s = s.replace(/^[a-z]+:\/\//i, "");
  }
  s = s.replace(/^[^/@]+@/, "");
  s = s.replace(/\.git$/, "").replace(/\/+$/, "");
  if (!/^[\w.-]+\.[\w.-]+\/.+/.test(s)) return null;
  return `https://${s}`;
}

export function buildCardModel(project: KusoProject, summary: ProjectSummaryItem | undefined): ProjectCardModel {
  const name = project.metadata.name;
  const allServices = summary?.services ?? [];
  // Env-group clones (staging etc.) are separate KusoService CRs; the
  // card is a production rollup.
  const services = allServices.filter((s) => {
    const g = s.metadata.labels?.[ENV_GROUP_LABEL];
    return !g || g === "production";
  });
  const envs = summary?.environments ?? [];

  const perService = services.map((s) => {
    const env = prodEnvFor(s, envs);
    return { svc: s, env, health: classifyEnv(s, env) };
  });
  const health = perService.map((p) => p.health);
  const status = summary ? rollupStatus(health) : "pending";
  const count = (h: ServiceHealth) => health.filter((x) => x === h).length;
  const stoppedCount = count("stopped");
  const attention = perService.find((p) => p.health === "failing") ?? perService.find((p) => p.health === "building");
  const detailText = attention?.env?.status?.stateDetail;

  // Same definitions as the canvas and the service overlay (facts.ts).
  const lastDeployAt = projectLastDeployedAt(envs);
  const primary = projectPrimaryDomain(project, services, envs);

  const domain = primary?.host;
  const domainHref = primary?.href;
  const publicURL = primary?.href;

  const repoRaw = project.spec.defaultRepo?.url ?? distinctServiceRepo(services);
  // Strip userinfo BEFORE display: deploy-token clone URLs carry a
  // working credential.
  const repo = stripRepoCredentials(repoRaw)?.replace(/^https?:\/\/(www\.)?/, "") || undefined;

  return {
    name,
    description: project.spec.description || undefined,
    createdAt: project.metadata.creationTimestamp,
    status,
    serviceCount: services.length,
    liveCount: count("live"),
    stoppedCount,
    readyOf: services.length - stoppedCount,
    failingCount: count("failing"),
    detail: typeof detailText === "string" && detailText ? detailText : undefined,
    lastDeployAt,
    domain,
    domainHref,
    repo,
    repoHref: repoWebURL(repoRaw) ?? undefined,
    publicURL,
    serviceNames: services.map(serviceShortName),
    metrics: summary?.metrics,
    allServiceCount: allServices.length,
    hasSummary: summary !== undefined,
  };
}

// Search haystack: name, description, repo, domain and service names.
export function matchesQuery(m: ProjectCardModel, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  const hay = [m.name, m.description, m.repo, m.domain, ...m.serviceNames]
    .filter((x): x is string => !!x)
    .join("\n")
    .toLowerCase();
  return needle.split(/\s+/).every((t) => hay.includes(t));
}

export type StatusFilter = "all" | "live" | "failing" | "building" | "sleeping" | "stopped";

export function matchesStatus(m: ProjectCardModel, f: StatusFilter): boolean {
  return f === "all" || m.status === f;
}

// MiB / GiB to match metrics-server's binary units.
export function formatBytes(b: number): string {
  if (b <= 0) return "0 MiB";
  const MiB = 1024 * 1024;
  const GiB = 1024 * MiB;
  if (b >= GiB) {
    const v = b / GiB;
    return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} GiB`;
  }
  return `${Math.round(b / MiB)} MiB`;
}
