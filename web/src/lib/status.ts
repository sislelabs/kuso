// The one status vocabulary. Every surface that names the state of a
// service, addon or job (canvas, list view, overlay headers, backups,
// health) takes its label and tone from here, so the same thing is never
// "Live" in one place and "Online" / "Healthy" / "ok" in another.

import { isProductionGroup, isProductionResource } from "@/lib/env-group";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { deriveServiceStatus, serviceBucketOf } from "@/features/services/nodeStatus";

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
  | "rollout_failing"
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
  // The new version fails (health check, crash, image pull) while the
  // previous one keeps serving: needs attention, but the app is up.
  rollout_failing: { label: "Rollout failing", tone: "warning" },
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
    case "rollout_failing":
      return "rollout_failing";
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
  // For image_pull: the registry's answer (not_found | unauthorized | unreachable).
  pullCause?: string;
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
    ...(typeof o.pullCause === "string" && o.pullCause ? { pullCause: o.pullCause } : {}),
  };
}

// The server's rollout facts (env.status.rollout): sent with state
// "rollout_failing", and with "deploying" while the previous version
// still serves (failing false).
export interface RolloutFacts {
  failing: boolean;
  // healthcheck | crash | image_pull | config_error | stalled
  kind?: string;
  // "the new version fails its health check (GET /health → 404)"
  cause?: string;
  previousServing: boolean;
  healthcheckPath?: string;
  // HTTP status code, "connection refused" or "timed out".
  probeResult?: string;
  // The registry's answer to a failed pull: not_found | unauthorized | unreachable.
  pullCause?: string;
  // Image refs: what the rollout moves to, and what still serves.
  newImage?: string;
  previousImage?: string;
  since?: string;
  // When the version still serving started (its newest ready pod).
  previousSince?: string;
}

export function rolloutFacts(status: Record<string, unknown> | undefined): RolloutFacts | undefined {
  const r = status?.rollout;
  if (!r || typeof r !== "object") return undefined;
  const o = r as Record<string, unknown>;
  const str = (k: string) => (typeof o[k] === "string" && o[k] ? (o[k] as string) : undefined);
  // Older servers sent rollout only while failing, without the flag.
  const failing = typeof o.failing === "boolean" ? o.failing : status?.state === "rollout_failing";
  const out: RolloutFacts = { failing, previousServing: o.previousServing === true };
  for (const k of [
    "kind",
    "cause",
    "healthcheckPath",
    "probeResult",
    "pullCause",
    "newImage",
    "previousImage",
    "since",
    "previousSince",
  ] as const) {
    const v = str(k);
    if (v) out[k] = v;
  }
  return out;
}

// imageTag is the tag of an image ref ("repo:tag" → "tag"), the short
// digest of "repo@sha256:…", or undefined for an untagged ref.
export function imageTag(ref: string | undefined): string | undefined {
  if (!ref) return undefined;
  const at = ref.lastIndexOf("@");
  if (at >= 0) return ref.slice(at + 1).replace(/^sha256:/, "").slice(0, 12) || undefined;
  const colon = ref.lastIndexOf(":");
  return colon > ref.lastIndexOf("/") ? ref.slice(colon + 1) || undefined : undefined;
}

// pullProblem names a failed image pull by the registry's answer: a 404
// is a typo'd or unpushed tag, a 401/403 is credentials.
export function pullProblem(pullCause: string | undefined, tag?: string): { short: string; hint: string } {
  switch (pullCause) {
    case "not_found":
      return {
        short: tag ? `tag ${tag} not found` : "image not found",
        hint: "The registry has no image with this tag. Check the tag for a typo, or push the image first.",
      };
    case "unauthorized":
      return {
        short: "registry denied access",
        hint: "The registry refused the pull (401/403). Add registry credentials in the project settings, or check that the repository name is right.",
      };
    case "unreachable":
      return {
        short: "registry unreachable",
        hint: "The cluster can't reach the registry. Check the registry host in the image name.",
      };
    default:
      return {
        short: tag ? `image ${tag} can't be pulled` : "image can't be pulled",
        hint: "Check the image name and tag, and that the registry allows the pull.",
      };
  }
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
  // The same derivation as every dot and pill (deriveServiceStatus), so
  // counts can't disagree with what the cards show.
  return serviceBucketOf(deriveServiceStatus({ stopped, env }));
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
