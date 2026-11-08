// Pure model behind the Health page's app-level rollup: "is everything I
// run OK?" folded from the batched projects summary plus three cheap
// per-project probes (addons, uptime, errors summary).

import type { ProjectSummaryItem } from "@/features/projects";
import type { ProjectUptime } from "@/features/uptime";
import type { ProjectErrorSummary } from "@/features/services/api";
import { serviceShortName } from "@/components/project/list/projectCardModel";
import { envGroupName } from "@/lib/env-group";
import {
  ADDON_STATUS,
  SERVICE_STATUS,
  serverStateCause,
  serviceStatusFromServerState,
  type StatusMeta,
} from "@/lib/status";
import type { KusoAddon, KusoEnvironment } from "@/types/projects";

// One project's probe results. null = that probe failed; "unsupported" =
// the server predates the endpoint (404).
export interface ProjectProbe {
  project: string;
  addons: KusoAddon[] | null;
  uptime: ProjectUptime | null;
  errors: ProjectErrorSummary | "unsupported" | null;
}

export type OverlayTab = "logs" | "deployments" | "errors";

export interface ServiceProblem {
  project: string;
  service: string;
  // Env group ("production", "staging", …).
  group: string;
  status: StatusMeta;
  cause?: string;
  detail?: string;
  href: string;
}

export interface UptimeProblem {
  project: string;
  service: string;
  status: StatusMeta;
  detail?: string;
  since?: string;
  stale: boolean;
  href: string;
}

export interface ErrorHotspot {
  project: string;
  service: string;
  occurrences: number;
  groups: number;
  lastSeen?: string;
  // undefined when the service no longer exists (errors outlive it).
  href?: string;
}

export interface AddonProblem {
  project: string;
  addon: string;
  status: StatusMeta;
  detail?: string;
  href: string;
}

export interface AppRollup {
  services: {
    // Non-preview deployments (production + env-group members).
    total: number;
    running: number;
    sleeping: number;
    stopped: number;
    inFlight: number;
    notDeployed: number;
    problems: ServiceProblem[];
  };
  uptime: {
    // false when the checker is off instance-wide; null = no project answered.
    enabled: boolean | null;
    checks: number;
    up: number;
    // Paused = the service is stopped; pending = not checked yet.
    paused: number;
    pending: number;
    problems: UptimeProblem[];
    unavailable: string[];
  };
  errors: {
    // False when every project's server 404'd the summary endpoint.
    supported: boolean;
    occurrences: number;
    groups: number;
    hotspots: ErrorHotspot[];
    unavailable: string[];
  };
  addons: {
    total: number;
    live: number;
    external: number;
    problems: AddonProblem[];
    unavailable: string[];
  };
}

export function projectHref(
  project: string,
  params: Record<string, string | undefined>,
): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) sp.set(k, v);
  const qs = sp.toString();
  return `/projects/${encodeURIComponent(project)}${qs ? `?${qs}` : ""}`;
}

// Failed or broken deploys are fixed from the deployment history; a
// crashloop or degraded runtime is read in the logs.
function tabForState(state: string | undefined): OverlayTab {
  return state === "build_failed" || state === "release_failed" ? "deployments" : "logs";
}

function stripPrefix(name: string, project: string): string {
  return name.startsWith(project + "-") ? name.slice(project.length + 1) : name;
}

function isPreviewGroup(group: string): boolean {
  return group.startsWith("preview");
}

function rollupServices(items: ProjectSummaryItem[]): AppRollup["services"] {
  const out: AppRollup["services"] = {
    total: 0,
    running: 0,
    sleeping: 0,
    stopped: 0,
    inFlight: 0,
    notDeployed: 0,
    problems: [],
  };
  for (const item of items) {
    const project = item.project.metadata.name;
    const byName = new Map(item.services.map((s) => [s.metadata.name, s]));
    for (const env of item.environments ?? []) {
      const group = envGroupName(env);
      if (isPreviewGroup(group)) continue;
      const svc = byName.get(env.spec.service);
      const service = svc ? serviceShortName(svc) : stripPrefix(env.spec.service, project);
      out.total++;
      if (svc?.spec.stopped === true) {
        out.stopped++;
        continue;
      }
      const state = stateOf(env);
      const kind = serviceStatusFromServerState(state);
      switch (kind) {
        case "live":
          out.running++;
          break;
        case "sleeping":
          out.sleeping++;
          break;
        case "stopped":
          out.stopped++;
          break;
        case "deploying":
        case "building":
          out.inFlight++;
          break;
        case "not_deployed":
          out.notDeployed++;
          break;
        case "failing":
        case "degraded": {
          const detail = env.status?.stateDetail;
          out.problems.push({
            project,
            service,
            group,
            status: SERVICE_STATUS[kind],
            cause: serverStateCause(state) ?? (kind === "degraded" ? "Not all replicas ready" : undefined),
            detail: typeof detail === "string" && detail ? detail : undefined,
            href: projectHref(project, {
              env: group === "production" ? undefined : group,
              service,
              tab: tabForState(state),
            }),
          });
          break;
        }
        // unknown / old server without a state: say nothing rather than guess.
        default:
          break;
      }
    }
  }
  // Hard failures first, then by name so the list doesn't reshuffle on poll.
  out.problems.sort(
    (a, b) =>
      rank(a.status) - rank(b.status) ||
      a.project.localeCompare(b.project) ||
      a.service.localeCompare(b.service) ||
      a.group.localeCompare(b.group),
  );
  return out;
}

function stateOf(env: KusoEnvironment): string | undefined {
  const s = env.status?.state;
  return typeof s === "string" ? s : undefined;
}

function rank(s: StatusMeta): number {
  return s.tone === "danger" ? 0 : s.tone === "warning" ? 1 : 2;
}

const UPTIME_DOWN: StatusMeta = { label: "Down", tone: "danger" };
const UPTIME_FAILING: StatusMeta = { label: "Failing", tone: "warning" };

function rollupUptime(probes: ProjectProbe[]): AppRollup["uptime"] {
  const out: AppRollup["uptime"] = {
    enabled: null,
    checks: 0,
    up: 0,
    paused: 0,
    pending: 0,
    problems: [],
    unavailable: [],
  };
  for (const p of probes) {
    if (!p.uptime) {
      out.unavailable.push(p.project);
      continue;
    }
    if (!p.uptime.enabled) {
      out.enabled = out.enabled ?? false;
      continue;
    }
    out.enabled = true;
    for (const s of p.uptime.services ?? []) {
      out.checks++;
      if (s.state === "up") out.up++;
      else if (s.state === "paused" || s.state === "disabled") out.paused++;
      else if (s.state === "pending") out.pending++;
      if (s.state !== "down" && s.state !== "failing") continue;
      const detail =
        s.reason || s.error || (s.statusCode ? `HTTP ${s.statusCode}` : undefined);
      out.problems.push({
        project: p.project,
        service: s.service,
        status: s.state === "down" ? UPTIME_DOWN : UPTIME_FAILING,
        detail,
        since: s.since,
        stale: s.stale === true,
        href: projectHref(p.project, { service: s.service, tab: "logs" }),
      });
    }
  }
  out.problems.sort((a, b) => rank(a.status) - rank(b.status) || a.project.localeCompare(b.project));
  return out;
}

function rollupErrors(probes: ProjectProbe[], items: ProjectSummaryItem[]): AppRollup["errors"] {
  const known = new Map(items.map((i) => [i.project.metadata.name, new Set(i.services.map(serviceShortName))]));
  const out: AppRollup["errors"] = { supported: false, occurrences: 0, groups: 0, hotspots: [], unavailable: [] };
  for (const p of probes) {
    if (p.errors === "unsupported") continue;
    if (!p.errors) {
      out.unavailable.push(p.project);
      continue;
    }
    out.supported = true;
    for (const [service, s] of Object.entries(p.errors.services ?? {})) {
      if (!s || s.occurrences <= 0) continue;
      out.occurrences += s.occurrences;
      out.groups += s.groups;
      out.hotspots.push({
        project: p.project,
        service,
        occurrences: s.occurrences,
        groups: s.groups,
        lastSeen: s.lastSeen || undefined,
        href:
          known.get(p.project)?.has(service) === false
            ? undefined
            : projectHref(p.project, { service, tab: "errors" }),
      });
    }
  }
  out.hotspots.sort((a, b) => b.occurrences - a.occurrences || a.project.localeCompare(b.project));
  return out;
}

type AddonCondition = { type?: string; status?: string; message?: string };

// Same reading as the canvas addon node: a failed helm release is
// failing, Deployed=True is live, anything else is still provisioning.
// Pod readiness isn't fetched here (one request per addon).
function rollupAddons(probes: ProjectProbe[]): AppRollup["addons"] {
  const out: AppRollup["addons"] = { total: 0, live: 0, external: 0, problems: [], unavailable: [] };
  for (const p of probes) {
    if (!p.addons) {
      out.unavailable.push(p.project);
      continue;
    }
    for (const a of p.addons) {
      out.total++;
      if (a.spec.external && !a.spec.pooler?.enabled) {
        out.external++;
        continue;
      }
      const conditions = (a.status?.conditions ?? []) as AddonCondition[];
      const failure = conditions.find(
        (c) => (c.type === "ReleaseFailed" || c.type === "Irreconcilable") && c.status === "True",
      );
      const deployed = conditions.some((c) => c.type === "Deployed" && c.status === "True");
      if (!failure && deployed) {
        out.live++;
        continue;
      }
      out.problems.push({
        project: p.project,
        addon: stripPrefix(a.metadata.name, p.project),
        status: failure ? ADDON_STATUS.failing : ADDON_STATUS.provisioning,
        detail: failure?.message || undefined,
        href: projectHref(p.project, { addon: a.metadata.name, tab: "overview" }),
      });
    }
  }
  out.problems.sort((a, b) => rank(a.status) - rank(b.status) || a.project.localeCompare(b.project));
  return out;
}

export function buildAppRollup(items: ProjectSummaryItem[], probes: ProjectProbe[]): AppRollup {
  return {
    services: rollupServices(items),
    uptime: rollupUptime(probes),
    errors: rollupErrors(probes, items),
    addons: rollupAddons(probes),
  };
}

export interface Verdict {
  tone: "success" | "warning" | "danger";
  headline: string;
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

// verdict is the page's single answer. Errors are reported in their row,
// not here: a healthy service can still log errors.
export interface ReconcileCounts {
  issues: number;
  critical: number;
}

export function verdict(r: AppRollup, reconcile: ReconcileCounts = { issues: 0, critical: 0 }): Verdict {
  const reconcileIssues = reconcile.issues;
  const parts: string[] = [];
  const svc = r.services.problems.length;
  if (svc > 0) parts.push(`${plural(svc, "service")} ${svc === 1 ? "needs" : "need"} attention`);
  const up = r.uptime.problems.length;
  if (up > 0) parts.push(`${plural(up, "uptime check")} failing`);
  const addonsFailing = r.addons.problems.filter((a) => a.status.tone === "danger").length;
  if (addonsFailing > 0) parts.push(`${plural(addonsFailing, "addon")} failing`);
  if (reconcileIssues > 0) parts.push(plural(reconcileIssues, "reconcile issue"));

  if (parts.length === 0) {
    const n = r.services.running;
    return {
      tone: "success",
      headline: n > 0 ? `All ${plural(n, "running service")} healthy` : "Nothing is failing",
    };
  }
  const danger =
    r.services.problems.some((p) => p.status.tone === "danger") ||
    r.uptime.problems.some((p) => p.status.tone === "danger") ||
    addonsFailing > 0 ||
    reconcile.critical > 0;
  return { tone: danger ? "danger" : "warning", headline: parts.join(" · ") };
}
