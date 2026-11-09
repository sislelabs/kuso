// One answer to "what state is this service / addon / cron in?" for the
// project canvas, the list view and the mobile incident view. Every
// visual signal on a node (dot, border, label) is driven from the
// NodeStatus returned here, so they can't disagree with each other.

import type { KusoAddon, KusoEnvironment } from "@/types/projects";
import type { BuildSummary } from "./api";
import {
  addonStatusFromServer,
  imageTag,
  pullProblem,
  rolloutFacts,
  stuckFacts,
  type RolloutFacts,
  type ServiceBucket,
  type StatusTone,
} from "@/lib/status";
import { promotedIfLive } from "@/features/projects/facts";

export type NodeStatusKind =
  | "live"
  | "degraded"
  | "building"
  | "deploying"
  | "build_failed"
  | "crashing"
  | "stuck"
  | "rollout_failing"
  | "sleeping"
  | "stopped"
  | "not_deployed"
  | "provisioning"
  | "paused"
  | "unknown";

export type NodeStatusTone = "success" | "warning" | "error" | "progress" | "info" | "muted";

export interface NodeStatus {
  kind: NodeStatusKind;
  label: string;
  tone: NodeStatusTone;
  // Server-provided human reason, when there is one.
  detail?: string;
}

const S = (kind: NodeStatusKind, label: string, tone: NodeStatusTone, detail?: string): NodeStatus =>
  detail ? { kind, label, tone, detail } : { kind, label, tone };

// Labels stick to the shared vocabulary (Live, Deploying, Building,
// Degraded, Failing, Sleeping, Stopped, Not deployed, Unknown); the
// specific cause ("Crashing", "Build failed") moves into the detail.
const because = (cause: string, detail?: string) => (detail ? `${cause}: ${detail}` : cause);

function serverState(state: string | undefined, detail?: string): NodeStatus | null {
  switch (state) {
    case "running":
      return S("live", "Live", "success", detail);
    case "degraded":
      return S("degraded", "Degraded", "warning", detail);
    // The new image wasn't promoted; the last green build keeps serving.
    case "release_failed":
      return S("degraded", "Degraded", "warning", because("Release failed", detail));
    case "crashlooping":
      return S("crashing", "Failing", "error", because("Crashing", detail));
    // Pods can't start or the rollout stalled; detail is the plain reason.
    case "stuck":
      return S("stuck", "Stuck", "error", detail);
    // The new version's pods fail while the previous version keeps
    // serving: degraded, not down. The detail names the cause.
    case "rollout_failing":
      return S("rollout_failing", "Rollout failing", "warning", detail);
    case "deploying":
      return S("deploying", "Deploying", "progress", detail);
    case "building":
      return S("building", "Building", "progress", detail);
    case "build_failed":
      return S("build_failed", "Failing", "error", because("Build failed", detail));
    case "sleeping":
      return S("sleeping", "Sleeping", "muted", detail);
    case "stopped":
      return S("stopped", "Stopped", "muted", detail);
    case "no_image":
      return S("not_deployed", "Not deployed", "info", detail);
    default:
      return null;
  }
}

export interface ServiceStatusInput {
  stopped?: boolean;
  env?: KusoEnvironment;
  latestBuild?: BuildSummary;
}

// deriveServiceStatus folds spec.stopped, the latest build for the env
// group and the env CR's status into one state. Priority:
//   1. spec.stopped — an explicit off wins over everything.
//   2. an in-flight build — the canvas polls builds faster than the
//      describe payload refreshes, so this is the freshest signal.
//   3. the server's unified rollup (env.status.state), which already
//      folds pod health in.
//   4. client heuristics for older servers that don't send state.
export function deriveServiceStatus({ stopped, env, latestBuild }: ServiceStatusInput): NodeStatus {
  if (stopped) return S("stopped", "Stopped", "muted");

  const build = (latestBuild?.status ?? "").toLowerCase();
  if (build === "pending" || build === "queued" || build === "running") {
    return S("building", "Building", "progress");
  }
  if (build === "deploying") return S("deploying", "Deploying", "progress");

  const detail = env?.status?.stateDetail || undefined;
  const fromServer = serverState(env?.status?.state, detail);
  // Name the commonest stuck cause outright rather than "Stuck".
  const stuck = fromServer?.kind === "stuck" ? stuckFacts(env?.status) : undefined;
  if (fromServer && stuck?.reason === "image_pull") {
    const tag = (env?.spec as { image?: { tag?: string } } | undefined)?.image?.tag;
    const named = stuck.pullCause ? capitalize(pullProblem(stuck.pullCause, tag).short) : fromServer.detail;
    return { ...fromServer, label: "Image pull failed", ...(named ? { detail: named } : {}) };
  }
  const rollout = fromServer ? rolloutFacts(env?.status) : undefined;
  // Until the new pods are ready the previous version is what serves:
  // name both instead of a bare "Deploying" (or, earlier, "Live").
  if (fromServer?.kind === "deploying" && rollout && !rollout.failing) {
    return S("deploying", "Rolling out", "progress", rollingOutDetail(rollout));
  }
  // A pull that fails while the old pods serve is stuck, not crashing;
  // the registry's answer says which kind of stuck.
  if (fromServer?.kind === "rollout_failing" && rollout?.kind === "image_pull") {
    const p = pullProblem(rollout.pullCause, imageTag(rollout.newImage));
    return S("rollout_failing", "Rollout stuck", "warning", `${capitalize(p.short)} (${servingNote(rollout)})`);
  }
  if (fromServer) return fromServer;

  if (!env) return S("not_deployed", "Not deployed", "info");
  if (env.status?.ready) return S("live", "Live", "success", detail);

  if (build === "failed" || build === "error") return S("build_failed", "Failing", "error", because("Build failed", detail));
  if (build === "release-failed") return S("build_failed", "Failing", "error", because("Release failed", detail));

  // A build-based service is held at 0 replicas until its first image
  // exists; that's "not deployed yet", not broken.
  if (!env.status?.imageTag && build !== "succeeded") return S("not_deployed", "Not deployed", "info", detail);

  const r = env.status?.replicas as { ready?: number; max?: number; desired?: number } | undefined;
  const want = r?.desired ?? r?.max ?? 0;
  if (want > 0 && (r?.ready ?? 0) === 0) return S("crashing", "Failing", "error", because("No ready pods", detail));

  const phase = String(env.status?.phase ?? "").toLowerCase();
  if (phase === "building") return S("building", "Building", "progress", detail);
  if (phase === "deploying") return S("deploying", "Deploying", "progress", detail);
  if (phase === "failed" || phase === "error") return S("crashing", "Failing", "error", detail);
  if (phase === "sleeping") return S("sleeping", "Sleeping", "muted", detail);
  return S("unknown", "Unknown", "muted", detail);
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// "v1.11.0 still serving" / "old version still serving".
function servingNote(r: RolloutFacts): string {
  if (!r.previousServing) return "nothing else is serving";
  const prev = imageTag(r.previousImage);
  return prev ? `${prev} still serving` : "old version still serving";
}

// "v9.9.9-nope · v1.11.0 still serving".
function rollingOutDetail(r: RolloutFacts): string {
  const next = imageTag(r.newImage);
  if (!r.previousServing) return next ? `${next} starting` : "Starting the new version";
  return next ? `${next} · ${servingNote(r)}` : capitalize(servingNote(r));
}

export interface PodSplitInput {
  current?: boolean;
  ready?: boolean;
  phase?: string;
}

// rolloutInFlight: the server says a rollout is under way (or failing)
// while the previous version still serves.
export function rolloutInFlight(env: KusoEnvironment | undefined): RolloutFacts | undefined {
  const state = env?.status?.state;
  if (state !== "deploying" && state !== "rollout_failing") return undefined;
  const r = rolloutFacts(env?.status);
  return r?.previousServing ? r : undefined;
}

// podServingSplit tells the previous version's serving pods apart from
// the new rollout's while one is in flight: "1 serving (previous) · 0/1
// new", instead of a "0/1 ready" that hides the pod still serving. Before
// the operator applies the change every pod is still the previous one.
export function podServingSplit(pods: readonly PodSplitInput[], desired?: number): string {
  const live = pods.filter((p) => p.phase !== "Succeeded" && p.phase !== "Failed");
  const previous = live.filter((p) => p.current === false);
  const prev = previous.length > 0 ? previous : live;
  const fresh = previous.length > 0 ? live.filter((p) => p.current !== false) : [];
  const want = Math.max(desired ?? 0, fresh.length);
  return `${prev.filter((p) => p.ready).length} serving (previous) · ${fresh.filter((p) => p.ready).length}/${want} new`;
}

// deriveAddonStatus reads the server's addon rollup (helm conditions +
// pod readiness, status.state) via the shared addonStatusFromServer, so
// the canvas, Health page and addon overview agree. Older servers fall
// back to the helm conditions alone.
export function deriveAddonStatus(addon: KusoAddon): NodeStatus {
  const { status, detail } = addonStatusFromServer(addon);
  switch (status) {
    case "live":
      return S("live", "Live", "success", detail);
    case "degraded":
      return S("degraded", "Degraded", "warning", detail);
    case "failing":
      return S("crashing", "Failing", "error", detail);
    case "stuck":
      return S("stuck", "Stuck", "error", detail);
    case "external":
      return S("live", "External", "info", detail);
    case "provisioning":
      return S("provisioning", "Provisioning", "progress", detail);
    default:
      return S("unknown", "Unknown", "muted", detail);
  }
}

export function deriveCronStatus(spec: { suspend?: boolean }): NodeStatus {
  return spec.suspend ? S("paused", "Paused", "muted") : S("live", "Scheduled", "success");
}

// lastDeployAt is when the currently served image went live — not when
// it was committed or when the newest (maybe failed) build finished.
// Takes the newest of the promotion stamp (written on every image
// promotion and survives build-CR retention), the operator's
// lastDeployedAt, and the latest build if it succeeded.
export function lastDeployAt(env: KusoEnvironment | undefined, latestBuild?: BuildSummary): string | undefined {
  const candidates: string[] = [];
  const promoted = env ? promotedIfLive(env) : undefined;
  if (promoted) candidates.push(promoted);
  if (env?.status?.lastDeployedAt) candidates.push(env.status.lastDeployedAt);
  if (env?.status?.rolloutCompletedAt) candidates.push(env.status.rolloutCompletedAt);
  if ((latestBuild?.status ?? "").toLowerCase() === "succeeded") {
    const t = latestBuild?.finishedAt ?? latestBuild?.startedAt;
    if (t) candidates.push(t);
  }
  let best: string | undefined;
  let bestT = -Infinity;
  for (const c of candidates) {
    const t = Date.parse(c);
    if (Number.isFinite(t) && t > bestT) {
      best = c;
      bestT = t;
    }
  }
  return best;
}

// Token classes per tone, shared by every surface that renders a status.
export const STATUS_TONE: Record<NodeStatusTone, { dot: string; text: string; border: string; chip: string }> = {
  success: {
    dot: "bg-[var(--success)]",
    text: "text-[var(--success)]",
    border: "border-[var(--success)]/60",
    chip: "border-[var(--success)]/30 bg-[var(--success-subtle)] text-[var(--success)]",
  },
  warning: {
    dot: "bg-[var(--warning)]",
    text: "text-[var(--warning)]",
    border: "border-[var(--warning)]/60",
    chip: "border-[var(--warning)]/30 bg-[var(--warning-subtle)] text-[var(--warning)]",
  },
  error: {
    dot: "bg-[var(--error)]",
    text: "text-[var(--error)]",
    border: "border-[var(--error)]/60",
    chip: "border-[var(--error)]/30 bg-[var(--error-subtle)] text-[var(--error)]",
  },
  progress: {
    dot: "bg-[var(--building)] motion-safe:animate-pulse",
    text: "text-[var(--building)]",
    border: "border-[var(--building)]/70",
    chip: "border-[var(--building)]/30 bg-[var(--building-subtle)] text-[var(--building)]",
  },
  info: {
    dot: "bg-[var(--info)]",
    text: "text-[var(--info)]",
    border: "border-[var(--info)]/50",
    chip: "border-[var(--info)]/30 bg-[var(--info-subtle)] text-[var(--info)]",
  },
  muted: {
    dot: "bg-[var(--text-tertiary)]",
    text: "text-[var(--text-secondary)]",
    border: "border-[var(--border-default)]",
    chip: "border-[var(--border-subtle)] bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
  },
};

// serviceBucketOf files a derived status into the count buckets the
// Health page and project cards sum, so a count never disagrees with the
// dot it counts. "attention" = anything with a warning or error tone.
export function serviceBucketOf(status: NodeStatus): ServiceBucket {
  switch (status.kind) {
    case "live":
      return "running";
    case "building":
    case "deploying":
    case "provisioning":
      return "in_flight";
    case "sleeping":
      return "sleeping";
    case "stopped":
    case "paused":
      return "stopped";
    case "not_deployed":
      return "not_deployed";
    case "unknown":
      return "unknown";
    default:
      return "attention";
  }
}

// NODE_TONE_TO_STATUS maps the node tones onto the lib/status tones.
export const NODE_TONE_TO_STATUS: Record<NodeStatusTone, StatusTone> = {
  success: "success",
  warning: "warning",
  error: "danger",
  progress: "progress",
  info: "info",
  muted: "neutral",
};
