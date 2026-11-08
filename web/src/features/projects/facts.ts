// One definition per project-level fact, so the canvas, project cards,
// overlay and deployments panel can't show different numbers for the
// same thing.

import { ENV_GROUP_LABEL, isProductionGroup } from "@/lib/env-group";
import { formatBytes } from "@/features/project-overview/api";
import { formatCpu } from "@/lib/format";
import { serviceUrl } from "@/features/services/serviceUrl";
import { hostScheme } from "@/lib/host-scheme";
import { serviceShortName } from "@/lib/utils";
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

// Custom hosts a service answers on in production: the env's
// additionalHosts (where custom domains live now), else spec.domains.
function customHosts(s: KusoService, env: KusoEnvironment | undefined): string[] {
  const fromEnv = (env?.spec.additionalHosts ?? []).filter((h): h is string => !!h);
  if (fromEnv.length) return fromEnv;
  return (s.spec.domains ?? []).map((d) => d?.host).filter((h): h is string => !!h);
}

// Registrable domain with no subdomain ("tickero.bg", "shop.co.uk").
const TWO_PART_SUFFIX = /^(co|com|org|net|ac|gov|edu)$/;
export function isApexHost(host: string): boolean {
  const labels = host.toLowerCase().replace(/\.$/, "").split(".");
  if (labels.length === 2) return true;
  return labels.length === 3 && TWO_PART_SUFFIX.test(labels[1]) && labels[2].length === 2;
}

function isLive(env: KusoEnvironment | undefined): boolean {
  return env?.status?.state === "running" || (!env?.status?.state && env?.status?.ready === true);
}

const FRONT_NAMES = new Set(["web", "frontend", "app"]);

// The project's headline domain, the URL of its primary public service.
// Only public production services with a port take part. Ranked by:
//   1. one of its custom hosts is the project base domain or an apex
//      (that host is shown, e.g. tickero.bg over api.tickero.bg);
//   2. its short name is the project's name, or web / frontend / app;
//   3. it has a custom domain;
//   4. it is live; then name.
// Otherwise the URL is exactly what the canvas node shows (serviceUrl:
// custom domain first, else the kuso auto host).
export function projectPrimaryDomain(
  project: KusoProject,
  services: ReadonlyArray<KusoService>,
  environments: ReadonlyArray<KusoEnvironment>,
): ProjectDomain | undefined {
  const name = project.metadata.name;
  const base = project.spec.baseDomain?.toLowerCase();
  const ranked = services
    .filter((s) => (!s.spec.project || s.spec.project === name) && isProductionService(s))
    .filter((s) => !s.spec.internal && (s.spec.port ?? 0) > 0)
    .map((s) => {
      const env = prodEnvOf(s, environments);
      // Services without a production env (staging clones) aren't public here.
      const hosts = env ? customHosts(s, env) : [];
      const baseHost = hosts.find((h) => h.toLowerCase() === base);
      const headline = baseHost ?? hosts.find(isApexHost);
      const short = serviceShortName(name, s.metadata.name);
      return {
        s,
        env,
        headline,
        // The base domain outranks any other apex.
        headlineRank: baseHost ? 2 : headline ? 1 : 0,
        named: short === name || FRONT_NAMES.has(short),
        custom: hosts.length > 0,
        live: isLive(env),
      };
    })
    .filter((c) => c.env)
    .sort(
      (a, b) =>
        b.headlineRank - a.headlineRank ||
        Number(b.named) - Number(a.named) ||
        Number(b.custom) - Number(a.custom) ||
        Number(b.live) - Number(a.live) ||
        a.s.metadata.name.localeCompare(b.s.metadata.name),
    );
  for (const c of ranked) {
    if (c.headline) {
      return { host: c.headline, href: `${hostScheme(c.env?.spec.tlsEnabled ?? true, c.headline)}://${c.headline}` };
    }
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
      // The unit (mCPU / cores) already names it.
      value: formatCpu(cpuMillicores),
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
      value: `${pods} service ${pods === 1 ? "pod" : "pods"}`,
      title: `Pods of ${where} reporting metrics now. Addons and cron jobs excluded.`,
    },
  ];
  if (uptime && uptime.total > 0) {
    parts.push({
      key: "uptime",
      value: `${uptime.up}/${uptime.total} health ${uptime.total === 1 ? "check" : "checks"} up`,
      title: "Production health checks passing on their latest probe. Paused and not-yet-run checks aren't counted.",
    });
  }
  const text = [scope, ...parts.map((p) => (p.label ? `${p.label} ${p.value}` : p.value))].join(" · ");
  return { scope, parts, text };
}

// cronsForEnv: crons run against the production env (the server resolves
// their image and env vars from "<service>-production"), so they belong
// on the production view. A non-production group shows only crons
// attached to a service cloned into that group.
export function cronsForEnv<C extends { spec: { service?: string } }>(
  crons: ReadonlyArray<C>,
  services: ReadonlyArray<KusoService>,
  envGroup: string,
  project: string,
): C[] {
  if (envGroup === "production") return [...crons];
  const groupServices = new Set<string>();
  for (const s of services) {
    if (s.metadata.labels?.[ENV_GROUP_LABEL] !== envGroup) continue;
    groupServices.add(s.metadata.name);
    groupServices.add(serviceShortName(project, s.metadata.name));
  }
  return crons.filter((c) => !!c.spec.service && groupServices.has(c.spec.service));
}
