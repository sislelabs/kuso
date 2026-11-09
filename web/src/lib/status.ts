// The one status vocabulary. Every surface that names the state of a
// service, addon or job (canvas, list view, overlay headers, backups,
// health) takes its label and tone from here, so the same thing is never
// "Live" in one place and "Online" / "Healthy" / "ok" in another.

import { isProductionGroup, isProductionResource } from "@/lib/env-group";
import type { KusoEnvironment, KusoService } from "@/types/projects";

export type StatusTone = "success" | "progress" | "warning" | "danger" | "info" | "neutral";

export interface StatusMeta {
  label: string;
  tone: StatusTone;
  // In-flight states pulse; settled ones don't.
  pulse?: boolean;
}

export type ServiceStatus =
  | "live"
  | "deploying"
  | "building"
  | "degraded"
  | "failing"
  | "stuck"
  | "sleeping"
  | "stopped"
  | "not_deployed"
  | "unknown";

export const SERVICE_STATUS: Record<ServiceStatus, StatusMeta> = {
  live: { label: "Live", tone: "success" },
  deploying: { label: "Deploying", tone: "progress", pulse: true },
  building: { label: "Building", tone: "progress", pulse: true },
  degraded: { label: "Degraded", tone: "warning" },
  failing: { label: "Failing", tone: "danger" },
  // Pods can't start (unschedulable, image pull, bad config) or a rollout
  // has been pending > 10 min. Needs attention like failing; the reason
  // is in stateDetail.
  stuck: { label: "Stuck", tone: "danger" },
  sleeping: { label: "Sleeping", tone: "neutral" },
  stopped: { label: "Stopped", tone: "neutral" },
  not_deployed: { label: "Not deployed", tone: "info" },
  unknown: { label: "Unknown", tone: "neutral" },
};

// serviceStatusFromServerState maps env.status.state (the server rollup in
// server-go/internal/projects/state.go) onto the vocabulary. The specific
// cause ("Crashlooping", "Build failed") belongs in the detail line, not
// the label. null = absent/unrecognised (old server).
export function serviceStatusFromServerState(state: string | undefined): ServiceStatus | null {
  switch (state) {
    case "running":
      return "live";
    case "deploying":
      return "deploying";
    case "building":
      return "building";
    // release_failed: the new image wasn't promoted; the last green build
    // keeps serving.
    case "degraded":
    case "release_failed":
      return "degraded";
    case "crashlooping":
    case "build_failed":
      return "failing";
    case "stuck":
      return "stuck";
    case "sleeping":
      return "sleeping";
    case "stopped":
      return "stopped";
    case "no_image":
      return "not_deployed";
    case "unknown":
      return "unknown";
    default:
      return null;
  }
}

// serverStateCause is the human cause shown next to a status when the
// label alone hides it (both crashloops and failed builds are "Failing").
export function serverStateCause(state: string | undefined): string | undefined {
  switch (state) {
    case "crashlooping":
      return "Crashlooping";
    case "build_failed":
      return "Latest build failed";
    case "release_failed":
      return "Release hook failed; previous build still serving";
    case "stuck":
      return "Can't start";
    default:
      return undefined;
  }
}

export type AddonStatus = "live" | "provisioning" | "degraded" | "failing" | "stuck" | "external" | "unknown";

export const ADDON_STATUS: Record<AddonStatus, StatusMeta> = {
  live: { label: "Live", tone: "success" },
  provisioning: { label: "Provisioning", tone: "progress", pulse: true },
  degraded: { label: "Degraded", tone: "warning" },
  failing: { label: "Failing", tone: "danger" },
  stuck: { label: "Stuck", tone: "danger" },
  // Managed outside kuso: kuso can't see its pods, so it has no health.
  external: { label: "External", tone: "info" },
  unknown: { label: "Unknown", tone: "neutral" },
};

// addonStatusFromPods folds the addon's pod readiness into the vocabulary.
// `failed` is the helm release condition (ReleaseFailed/Irreconcilable);
// `deployed` is Deployed=True, which turns "no pods" from still-starting
// into down.
export function addonStatusFromPods(input: {
  external?: boolean;
  failed?: boolean;
  deployed?: boolean;
  total: number;
  ready: number;
}): AddonStatus {
  if (input.external) return "external";
  if (input.failed) return "failing";
  if (input.total === 0) return input.deployed ? "failing" : "provisioning";
  if (input.ready === input.total) return "live";
  if (input.ready === 0) return "failing";
  return "degraded";
}

// The server's stuck facts (env.status.stuck / addon status.stuck).
export interface StuckFacts {
  // unschedulable | image_pull | config_error | crashloop | rollout_stalled
  reason: string;
  summary: string;
  // Raw scheduler / kubelet text, for a details disclosure.
  message?: string;
  since?: string;
}

export function stuckFacts(status: Record<string, unknown> | undefined): StuckFacts | undefined {
  const s = status?.stuck;
  if (!s || typeof s !== "object") return undefined;
  const o = s as Record<string, unknown>;
  if (typeof o.reason !== "string" || typeof o.summary !== "string") return undefined;
  return {
    reason: o.reason,
    summary: o.summary,
    ...(typeof o.message === "string" && o.message ? { message: o.message } : {}),
    ...(typeof o.since === "string" && o.since ? { since: o.since } : {}),
  };
}

type HelmCondition = { type?: string; status?: string; message?: string };

export interface AddonHealthFacts {
  status: AddonStatus;
  detail?: string;
  // Live pod counts, when the server sent them.
  pods?: { ready: number; total: number };
}

const ADDON_SERVER_STATES = new Set<AddonStatus>(["live", "provisioning", "degraded", "failing", "stuck", "external"]);

// addonStatusFromServer is the one reading of an addon's health from the
// addon list: the server's status.state (pod readiness folded in) when
// present, else the helm conditions alone (older servers).
export function addonStatusFromServer(addon: {
  spec: { external?: unknown; pooler?: { enabled?: boolean } };
  status?: Record<string, unknown>;
}): AddonHealthFacts {
  const st = addon.status;
  const state = st?.state;
  const detail = typeof st?.stateDetail === "string" && st.stateDetail ? st.stateDetail : undefined;
  const p = st?.pods as { ready?: unknown; total?: unknown } | undefined;
  const pods =
    p && typeof p.ready === "number" && typeof p.total === "number" ? { ready: p.ready, total: p.total } : undefined;
  if (typeof state === "string" && ADDON_SERVER_STATES.has(state as AddonStatus)) {
    return { status: state as AddonStatus, ...(detail ? { detail } : {}), ...(pods ? { pods } : {}) };
  }
  if (addon.spec.external && !addon.spec.pooler?.enabled) return { status: "external", detail: "Managed outside kuso" };
  const conditions = (st?.conditions ?? []) as HelmCondition[];
  const failure = conditions.find(
    (c) => (c.type === "ReleaseFailed" || c.type === "Irreconcilable") && c.status === "True",
  );
  if (failure) return { status: "failing", detail: failure.message || "Helm release failed" };
  if (conditions.some((c) => c.type === "Deployed" && c.status === "True")) return { status: "live" };
  return { status: "provisioning" };
}

// One answer per service env for every count (Health page, project cards,
// project filters). "attention" = failing, stuck or degraded.
export type ServiceBucket = "running" | "in_flight" | "attention" | "sleeping" | "stopped" | "not_deployed" | "unknown";

export function serviceBucket(stopped: boolean | undefined, env: KusoEnvironment | undefined): ServiceBucket {
  if (stopped) return "stopped";
  if (!env) return "not_deployed";
  const state = env.status?.state;
  const kind = serviceStatusFromServerState(typeof state === "string" ? state : undefined);
  switch (kind) {
    case "live":
      return "running";
    case "deploying":
    case "building":
      return "in_flight";
    case "failing":
    case "stuck":
    case "degraded":
      return "attention";
    case "sleeping":
      return "sleeping";
    case "stopped":
      return "stopped";
    case "not_deployed":
      return "not_deployed";
    case "unknown":
      return "unknown";
  }
  // Older server without a rollup state.
  const replicas = env.status?.replicas as { ready?: number } | undefined;
  return (replicas?.ready ?? 0) > 0 || env.status?.ready === true ? "running" : "unknown";
}

export type ServiceTally = Record<ServiceBucket, number> & { total: number };

// tallyServices counts the PRODUCTION env of each production service, the
// scope project cards use, so the Health page and the cards sum the same.
export function tallyServices(
  services: ReadonlyArray<KusoService>,
  envs: ReadonlyArray<KusoEnvironment>,
): ServiceTally {
  const out: ServiceTally = {
    total: 0,
    running: 0,
    in_flight: 0,
    attention: 0,
    sleeping: 0,
    stopped: 0,
    not_deployed: 0,
    unknown: 0,
  };
  for (const s of services) {
    if (!isProductionResource(s.metadata.labels)) continue;
    const env = envs.find((e) => e.spec.service === s.metadata.name && isProductionGroup(e));
    out.total++;
    out[serviceBucket(s.spec.stopped, env)]++;
  }
  return out;
}

// Job-shaped things (backups, crons, runs, restores).
export type RunStatus = "succeeded" | "running" | "pending" | "failed" | "skipped" | "never";

export const RUN_STATUS: Record<RunStatus, StatusMeta> = {
  succeeded: { label: "Succeeded", tone: "success" },
  running: { label: "Running", tone: "progress", pulse: true },
  pending: { label: "Pending", tone: "neutral" },
  failed: { label: "Failed", tone: "danger" },
  skipped: { label: "Skipped", tone: "neutral" },
  never: { label: "Never run", tone: "neutral" },
};

// Token classes per tone. One table so a "warning" dot, chip and text are
// the same colour everywhere.
export const TONE_CLASSES: Record<StatusTone, { dot: string; text: string; chip: string; border: string }> = {
  success: {
    dot: "bg-[var(--success)]",
    text: "text-[var(--success)]",
    chip: "border-[var(--success)]/30 bg-[var(--success-subtle)] text-[var(--success)]",
    border: "border-[var(--success)]/40",
  },
  progress: {
    dot: "bg-[var(--building)]",
    text: "text-[var(--building)]",
    chip: "border-[var(--building)]/30 bg-[var(--building-subtle)] text-[var(--building)]",
    border: "border-[var(--building)]/40",
  },
  warning: {
    dot: "bg-[var(--warning)]",
    text: "text-[var(--warning)]",
    chip: "border-[var(--warning)]/30 bg-[var(--warning-subtle)] text-[var(--warning)]",
    border: "border-[var(--warning)]/40",
  },
  danger: {
    dot: "bg-[var(--danger)]",
    text: "text-[var(--danger)]",
    chip: "border-[var(--danger)]/30 bg-[var(--danger-subtle)] text-[var(--danger)]",
    border: "border-[var(--danger)]/40",
  },
  info: {
    dot: "bg-[var(--info)]",
    text: "text-[var(--info)]",
    chip: "border-[var(--info)]/30 bg-[var(--info-subtle)] text-[var(--info)]",
    border: "border-[var(--info)]/40",
  },
  neutral: {
    dot: "bg-[var(--text-tertiary)]",
    text: "text-[var(--text-secondary)]",
    chip: "border-[var(--border-subtle)] bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
    border: "border-[var(--border-default)]",
  },
};
