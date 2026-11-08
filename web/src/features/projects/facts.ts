// One definition per project-level fact, so the canvas, project cards,
// overlay and deployments panel can't show different numbers for the
// same thing.

import { ENV_GROUP_LABEL, isProductionGroup } from "@/lib/env-group";
import { formatBytes, formatMillicores } from "@/features/project-overview/api";
import { serviceUrl } from "@/features/services/serviceUrl";
import type { KusoEnvironment, KusoProject, KusoService } from "@/types/projects";

export const PROMOTED_AT_ANNOTATION = "kuso.sislelabs.com/promoted-at";

function newest(values: ReadonlyArray<string | undefined>): string | undefined {
  let best: string | undefined;
  let bestMs = -Infinity;
  for (const v of values) {
    if (!v) continue;
    const ms = Date.parse(v);
    if (Number.isFinite(ms) && ms > bestMs) {
      best = v;
      bestMs = ms;
    }
  }
  return best;
}

// When the env's current release went live: the newest of the promotion
// stamp (written on every image promotion, survives build-CR retention)
// and the operator's lastDeployedAt. Build finish times are deliberately
// ignored — a succeeded build isn't live until it's promoted.
export function lastDeployedAt(env: KusoEnvironment | undefined): string | undefined {
  if (!env) return undefined;
  return newest([env.metadata?.annotations?.[PROMOTED_AT_ANNOTATION], env.status?.lastDeployedAt]);
}

// Newest lastDeployedAt across the production env-group.
export function projectLastDeployedAt(envs: ReadonlyArray<KusoEnvironment>): string | undefined {
  return newest(envs.filter(isProductionGroup).map(lastDeployedAt));
}

export interface ProjectDomain {
  host: string;
  href: string;
}

function isProductionService(s: KusoService): boolean {
  const g = s.metadata.labels?.[ENV_GROUP_LABEL];
  return !g || g === "production";
}

function prodEnvOf(s: KusoService, envs: ReadonlyArray<KusoEnvironment>): KusoEnvironment | undefined {
  return envs.find((e) => e.spec.service === s.metadata.name && isProductionGroup(e));
}

function hasCustomDomain(s: KusoService, env: KusoEnvironment | undefined): boolean {
  return !!env?.spec.additionalHosts?.some((h) => !!h) || !!s.spec.domains?.some((d) => !!d?.host);
}

function isLive(env: KusoEnvironment | undefined): boolean {
  return env?.status?.state === "running" || (!env?.status?.state && env?.status?.ready === true);
}

// The project's headline domain: the primary public service's URL exactly
// as the canvas node and service overlay show it (serviceUrl on its
// production env — custom domain first, else the kuso auto host).
// Primary public service = a non-internal production service with a port,
// ranked by: has a custom domain, is live, then name.
export function projectPrimaryDomain(
  project: KusoProject,
  services: ReadonlyArray<KusoService>,
  environments: ReadonlyArray<KusoEnvironment>,
): ProjectDomain | undefined {
  const name = project.metadata.name;
  const ranked = services
    .filter((s) => (!s.spec.project || s.spec.project === name) && isProductionService(s))
    .filter((s) => !s.spec.internal && (s.spec.port ?? 0) > 0)
    .map((s) => {
      const env = prodEnvOf(s, environments);
      return { s, env, custom: hasCustomDomain(s, env), live: isLive(env) };
    })
    .sort(
      (a, b) =>
        Number(b.custom) - Number(a.custom) ||
        Number(b.live) - Number(a.live) ||
        a.s.metadata.name.localeCompare(b.s.metadata.name),
    );
  for (const c of ranked) {
    const href = serviceUrl(c.s, c.env);
    if (!href) continue;
    try {
      return { host: new URL(href).host, href };
    } catch {
      continue;
    }
  }
  return undefined;
}

export interface ProjectStatsPart {
  key: "cpu" | "mem" | "pods" | "uptime";
  // Undefined when the value reads on its own ("6 pods").
  label?: string;
  value: string;
  title: string;
}

export interface ProjectStatsLabel {
  scope: string;
  parts: ProjectStatsPart[];
  text: string;
}

export interface ProjectStatsInput {
  // Env-group the numbers were summed over (useProjectMetrics' env).
  env: string;
  cpuMillicores: number;
  memBytes: number;
  pods: number;
  // Uptime checks only run against production; pass it only there.
  uptime?: { up: number; total: number };
}

export function envScopeLabel(env: string): string {
  return env === "production" ? "Production" : env;
}

// Cores throughout, so "0.1 cores" and "1.4 cores" read on one scale
// (formatMillicores alone switches unit at 1000m).
export function formatCores(m: number): string {
  if (m <= 0) return "0 cores";
  if (m < 10) return "<0.01 cores";
  const v = m < 1000 ? (m / 1000).toFixed(2).replace(/\.?0+$/, "") : formatMillicores(m);
  return `${v} ${v === "1" ? "core" : "cores"}`;
}

// The canvas header strip. Scope mirrors GET /projects/{p}/metrics: pods
// whose instance is an env CR in this env-group, i.e. every service's
// deployment; addons and cron/run jobs are separate releases and aren't
// counted. Pods = pods metrics-server is reporting for, right now.
export function projectStatsLabel({ env, cpuMillicores, memBytes, pods, uptime }: ProjectStatsInput): ProjectStatsLabel {
  const scope = envScopeLabel(env);
  const where = `every ${env} service`;
  const parts: ProjectStatsPart[] = [
    {
      key: "cpu",
      label: "CPU",
      value: formatCores(cpuMillicores),
      title: `CPU in use now, summed over the running pods of ${where}. Addons and cron jobs excluded.`,
    },
    {
      key: "mem",
      label: "Mem",
      value: formatBytes(memBytes),
      title: `Memory in use now, summed over the running pods of ${where}. Addons and cron jobs excluded.`,
    },
    {
      key: "pods",
      value: `${pods} ${pods === 1 ? "pod" : "pods"}`,
      title: `Pods of ${where} reporting metrics now. Addons and cron jobs excluded.`,
    },
  ];
  if (uptime && uptime.total > 0) {
    parts.push({
      key: "uptime",
      value: `${uptime.up}/${uptime.total} uptime ${uptime.total === 1 ? "check" : "checks"} up`,
      title: "Production uptime checks passing on their latest probe.",
    });
  }
  const text = [scope, ...parts.map((p) => (p.label ? `${p.label} ${p.value}` : p.value))].join(" · ");
  return { scope, parts, text };
}
