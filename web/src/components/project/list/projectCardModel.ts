import { stripRepoCredentials } from "@/lib/format";
import { isProductionGroup, isProductionResource } from "@/lib/env-group";
import { projectLastDeployedAt, projectPrimaryDomain, type ProjectDomain } from "@/features/projects/facts";
import type { KusoEnvironment, KusoProject, KusoService } from "@/types/projects";
import type { ProjectSummaryItem, ProjectSummaryMetrics } from "@/features/projects";
import { SERVICE_STATUS, type ServiceStatus, type StatusTone } from "@/lib/status";
import { deriveServiceStatus } from "@/features/services/nodeStatus";
import { nodeServiceStatus } from "@/components/service/workspace/statusMeta";

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
  // Per-service status in the shared vocabulary (lib/status), so the card
  // names a service exactly what the canvas node calls it.
  counts: Partial<Record<ServiceStatus, number>>;
  // Worst tone across the services: the card's dot and rail colour.
  tone: StatusTone;
  // stateDetail of the first failing/building service, for the tooltip.
  detail?: string;
  // Latest promotion (build → live) across production envs. Undefined
  // when nothing has deployed yet.
  lastDeployAt?: string;
  domain?: string;
  domainHref?: string;
  // The full pick, so the card can swap in the auto host while a custom
  // domain isn't serving (useLiveProjectDomain).
  primaryDomain?: ProjectDomain;
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

// envServiceStatus is the canvas node's status (deriveServiceStatus, the
// one selector), bucketed into the shared vocabulary for counting.
export function envServiceStatus(svc: KusoService, env: KusoEnvironment | undefined): ServiceStatus {
  return nodeServiceStatus(deriveServiceStatus({ stopped: svc.spec.stopped === true, env }));
}

// Failing, stuck and degraded all need attention, so they land in "failing".
const STATUS_HEALTH: Record<ServiceStatus, ServiceHealth> = {
  live: "live",
  deploying: "building",
  building: "building",
  degraded: "failing",
  failing: "failing",
  stuck: "failing",
  sleeping: "sleeping",
  stopped: "stopped",
  not_deployed: "pending",
  unknown: "pending",
};

export function serviceHealth(svc: KusoService, env: KusoEnvironment | undefined): ServiceHealth {
  return STATUS_HEALTH[envServiceStatus(svc, env)] ?? "pending";
}

const TONE_RANK: Record<StatusTone, number> = { danger: 5, warning: 4, progress: 3, success: 2, info: 1, neutral: 0 };

export function worstTone(statuses: ReadonlyArray<ServiceStatus>): StatusTone {
  let best: StatusTone = "neutral";
  for (const s of statuses) {
    const t = SERVICE_STATUS[s]?.tone ?? "neutral";
    if (TONE_RANK[t] > TONE_RANK[best]) best = t;
  }
  return best;
}

// Order the non-live parts are listed in, most urgent first.
const SUMMARY_ORDER: ReadonlyArray<ServiceStatus> = [
  "failing",
  "stuck",
  "degraded",
  "deploying",
  "building",
  "sleeping",
  "stopped",
  "not_deployed",
  "unknown",
];

export interface StatusPart {
  text: string;
  // The status this part counts; undefined for the "x of n live" lead.
  status?: ServiceStatus;
}

// projectStatusParts is the card's one status line, e.g.
// "1 of 3 live · 1 failing · 1 stopped". Counts are of production services.
export function projectStatusParts(counts: Partial<Record<ServiceStatus, number>>, total: number): StatusPart[] {
  if (total === 0) return [{ text: "No services" }];
  const live = counts.live ?? 0;
  if (live === total) return [{ text: total === 1 ? "Live" : `All ${total} live`, status: "live" }];
  const others = (Object.keys(counts) as ServiceStatus[]).filter((k) => k !== "live" && (counts[k] ?? 0) > 0);
  // A project where every service shares one non-live state reads as that state.
  if (live === 0 && others.length === 1) {
    const label = SERVICE_STATUS[others[0]]?.label ?? others[0];
    return total === 1
      ? [{ text: label, status: others[0] }]
      : [{ text: label, status: others[0] }, { text: `${total} services` }];
  }
  const ordered = [
    ...SUMMARY_ORDER.filter((s) => others.includes(s)),
    ...others.filter((s) => !SUMMARY_ORDER.includes(s)),
  ];
  return [
    { text: `${live} of ${total} live` },
    ...ordered.map((s) => ({ text: `${counts[s]} ${(SERVICE_STATUS[s]?.label ?? s).toLowerCase()}`, status: s })),
  ];
}

export function projectStatusSummary(counts: Partial<Record<ServiceStatus, number>>, total: number): string {
  return projectStatusParts(counts, total)
    .map((p) => p.text)
    .join(" · ");
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
  const services = allServices.filter((s) => isProductionResource(s.metadata.labels));
  const envs = summary?.environments ?? [];

  const perService = services.map((s) => {
    const env = prodEnvFor(s, envs);
    return { svc: s, env, st: envServiceStatus(s, env), health: serviceHealth(s, env) };
  });
  const health = perService.map((p) => p.health);
  const counts: Partial<Record<ServiceStatus, number>> = {};
  for (const p of perService) counts[p.st] = (counts[p.st] ?? 0) + 1;
  const status = summary ? rollupStatus(health) : "pending";
  const countOf = (h: ServiceHealth) => health.filter((x) => x === h).length;
  const stoppedCount = countOf("stopped");
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
    liveCount: countOf("live"),
    stoppedCount,
    readyOf: services.length - stoppedCount,
    failingCount: countOf("failing"),
    counts,
    tone: summary ? worstTone(perService.map((p) => p.st)) : "neutral",
    detail: typeof detailText === "string" && detailText ? detailText : undefined,
    lastDeployAt,
    domain,
    domainHref,
    primaryDomain: primary,
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
