// One definition per project-level fact, so the canvas, project cards,
// overlay and deployments panel can't show different numbers for the
// same thing.

import { ENV_GROUP_LABEL, isProductionGroup } from "@/lib/env-group";
import { formatBytes } from "@/features/project-overview/api";
import { formatCpu } from "@/lib/format";
import { serviceUrl } from "@/features/services/serviceUrl";
import { hostScheme } from "@/lib/host-scheme";
import { rolloutFacts } from "@/lib/status";
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

// When the env's last rollout went live: the newest of the promotion
// stamp (written on every image promotion, survives build-CR retention),
// the operator's lastDeployedAt, and rolloutCompletedAt — when the pods'
// current ReplicaSet finished rolling out, so config changes, image
// changes and rollbacks count as deploys too. Build finish times are
// deliberately ignored: a succeeded build isn't live until it's promoted.
export function lastDeployedAt(env: KusoEnvironment | undefined): string | undefined {
  if (!env) return undefined;
  const best = newest([promotedIfLive(env), env.status?.lastDeployedAt, env.status?.rolloutCompletedAt]);
  // Mid failing rollout the completion stamp is gone; the serving
  // version's pod start is the closest record of when it went out.
  const computed =
    best ?? (env.status?.state === "rollout_failing" ? rolloutFacts(env.status)?.previousSince : undefined);
  // A rollout in flight drops the completion stamp, so the value would
  // fall back to an older one ("14h ago") until the new pods are ready.
  // Hold the last settled value instead; it moves when the rollout lands.
  const key = env.metadata?.uid || `${env.metadata?.namespace ?? ""}/${env.metadata?.name ?? ""}`;
  if (env.status?.state === "deploying") {
    const held = settledDeploy.get(key);
    if (held) return !computed || Date.parse(held) > Date.parse(computed) ? held : computed;
    // Nothing ready means nothing from any deploy is serving yet (a first
    // deploy): its own stamps don't count until the pods are up.
    const ready = (env.status?.replicas as { ready?: number } | undefined)?.ready ?? 0;
    return ready > 0 ? computed : undefined;
  }
  if (computed) settledDeploy.set(key, computed);
  return computed;
}

const settledDeploy = new Map<string, string>();

// promotedIfLive drops a promotion stamp whose rollout never finished: an
// image that can't be pulled or a version failing its health check was
// promoted, but it isn't what's serving, so it isn't "deployed".
export function promotedIfLive(env: KusoEnvironment): string | undefined {
  const promoted = env.metadata?.annotations?.[PROMOTED_AT_ANNOTATION];
  const at = Date.parse(promoted ?? "");
  if (!promoted || !Number.isFinite(at)) return promoted;
  const done = Date.parse(env.status?.rolloutCompletedAt ?? "");
  if (Number.isFinite(done) && done >= at) return promoted;
  const state = env.status?.state;
  if (state === "rollout_failing") {
    // Only the promotion that started the failing rollout is unfinished;
    // an older one is what's still serving.
    const since = Date.parse(rolloutFacts(env.status)?.since ?? "");
    return Number.isFinite(since) && at >= since - 60_000 ? undefined : promoted;
  }
  if (state === "stuck") {
    const ready = (env.status?.replicas as { ready?: number } | undefined)?.ready ?? 0;
    return ready > 0 ? promoted : undefined;
  }
  // The promoted image is still rolling out over the previous version.
  if (state === "deploying" && rolloutFacts(env.status)) return undefined;
  return promoted;
}

// Newest lastDeployedAt across the production env-group.
export function projectLastDeployedAt(envs: ReadonlyArray<KusoEnvironment>): string | undefined {
  return newest(envs.filter(isProductionGroup).map(lastDeployedAt));
}

export interface ProjectDomain {
  host: string;
  href: string;
  // Set when host is a custom domain: the service it belongs to (short
  // name) and the kuso auto host to show instead while the custom one
  // isn't serving yet (DNS not pointed, cert not issued).
  custom?: { service: string; fallback?: { host: string; href: string } };
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
    const short = serviceShortName(name, c.s.metadata.name);
    const autoHost = c.env?.spec.host;
    const fallback = autoHost
      ? { host: autoHost, href: `${hostScheme(c.env?.spec.tlsEnabled ?? true, autoHost)}://${autoHost}` }
      : undefined;
    if (c.headline) {
      return {
        host: c.headline,
        href: `${hostScheme(c.env?.spec.tlsEnabled ?? true, c.headline)}://${c.headline}`,
        custom: { service: short, fallback },
      };
    }
    const href = serviceUrl(c.s, c.env);
    if (!href) continue;
    try {
      const host = new URL(href).host;
      return { host, href, ...(c.custom && host !== autoHost ? { custom: { service: short, fallback } } : {}) };
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
      value: `${uptime.up}/${uptime.total} uptime ${uptime.total === 1 ? "check" : "checks"} up`,
      title: "Production health checks passing on their latest probe. Paused and not-yet-run checks aren't counted.",
    });
  }
  const text = [scope, ...parts.map((p) => (p.label ? `${p.label} ${p.value}` : p.value))].join(" · ");
  return { scope, parts, text };
}

export interface ServiceIssues {
  // Failing, crashing or stuck: the node's error tone.
  failing: number;
  stuck: number;
  degraded: number;
  // "1 service failing · 1 stuck"; undefined when nothing needs attention.
  text?: string;
  tone: "error" | "warning" | "ok";
}

// serviceIssues counts the services that need attention from the same
// per-service status the canvas nodes show (deriveServiceStatus), so a
// project headline can't read healthy while a node is red.
export function serviceIssues(statuses: ReadonlyArray<{ kind: string; tone: string }>): ServiceIssues {
  let failing = 0;
  let stuck = 0;
  let degraded = 0;
  for (const s of statuses) {
    if (s.kind === "stuck") stuck++;
    else if (s.tone === "error") failing++;
    else if (s.kind === "degraded") degraded++;
  }
  const parts: string[] = [];
  if (failing > 0) parts.push(`${failing} ${failing === 1 ? "service" : "services"} failing`);
  if (stuck > 0) parts.push(failing > 0 ? `${stuck} stuck` : `${stuck} ${stuck === 1 ? "service" : "services"} stuck`);
  if (degraded > 0) parts.push(parts.length > 0 ? `${degraded} degraded` : `${degraded} ${degraded === 1 ? "service" : "services"} degraded`);
  return {
    failing,
    stuck,
    degraded,
    text: parts.length > 0 ? parts.join(" · ") : undefined,
    tone: failing + stuck > 0 ? "error" : degraded > 0 ? "warning" : "ok",
  };
}

export interface ProjectHealthLine {
  // "2 of 3 services checked · 1 failing"; undefined when there's nothing to say.
  text?: string;
  // The two halves of text: uptime coverage, and what needs attention.
  coverage?: string;
  issues?: string;
  tone: "error" | "warning" | "ok";
  // Services needing attention, worst first (failing, then degraded).
  problems: string[];
}

// projectHealthLine is the project headline: how many services the uptime
// ping covers and how many need attention. A service counts as failing
// when its status (the canvas node's) or its uptime check says so, so the
// line can't read "2/2 up" next to a red node the ping never reaches.
// A failed build with nothing serving is named as such: next to the uptime
// coverage, "1 failing" read as an outage of a service that never deployed.
export function projectHealthLine(
  services: ReadonlyArray<{ short: string; status: { kind: string; tone: string } }>,
  // null when uptime checks are off or unavailable.
  uptime: ReadonlyArray<{ service: string; state: string }> | null,
): ProjectHealthLine {
  const names = new Set(services.map((s) => s.short));
  const checks = (uptime ?? []).filter((u) => names.has(u.service));
  const checked = new Set(checks.filter((u) => u.state === "up" || u.state === "failing" || u.state === "down").map((u) => u.service));
  const pingBad = new Set(checks.filter((u) => u.state === "failing" || u.state === "down").map((u) => u.service));
  const failing: string[] = [];
  const buildFailed: string[] = [];
  const degraded: string[] = [];
  for (const s of services) {
    if (s.status.kind === "build_failed" && !pingBad.has(s.short)) buildFailed.push(s.short);
    else if (s.status.tone === "error" || s.status.kind === "stuck" || pingBad.has(s.short)) failing.push(s.short);
    else if (s.status.kind === "degraded" || s.status.kind === "rollout_failing" || s.status.tone === "warning") degraded.push(s.short);
  }
  const covered = !!uptime && checked.size > 0;
  const parts: string[] = [];
  // Without the coverage lead-in the first count names its noun.
  const count = (n: number) => (parts.length > 0 ? `${n}` : `${n} ${n === 1 ? "service" : "services"}`);
  if (covered) parts.push(`${checked.size} of ${services.length} ${services.length === 1 ? "service" : "services"} checked`);
  if (failing.length > 0) parts.push(`${count(failing.length)} failing`);
  if (buildFailed.length > 0) parts.push(`${buildFailed.length} ${buildFailed.length === 1 ? "build" : "builds"} failed`);
  if (degraded.length > 0) parts.push(`${count(degraded.length)} degraded`);
  if (covered && parts.length === 1) parts.push("all up");
  const issues = (covered ? parts.slice(1) : parts).filter((p) => p !== "all up");
  return {
    text: parts.length > 0 ? parts.join(" · ") : undefined,
    coverage: covered ? parts[0] : undefined,
    issues: issues.length > 0 ? issues.join(" · ") : undefined,
    tone: failing.length + buildFailed.length > 0 ? "error" : degraded.length > 0 ? "warning" : "ok",
    problems: [...failing, ...buildFailed, ...degraded],
  };
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
