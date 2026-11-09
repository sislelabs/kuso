// One answer to "what state is this service / addon / cron in?" for the
// project canvas, the list view and the mobile incident view. Every
// visual signal on a node (dot, border, label) is driven from the
// NodeStatus returned here, so they can't disagree with each other.

import type { KusoAddon, KusoEnvironment } from "@/types/projects";
import type { BuildSummary } from "./api";
import { addonStatusFromServer } from "@/lib/status";

export type NodeStatusKind =
  | "live"
  | "degraded"
  | "building"
  | "deploying"
  | "build_failed"
  | "crashing"
  | "stuck"
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
  const promoted = env?.metadata?.annotations?.["kuso.sislelabs.com/promoted-at"];
  if (promoted) candidates.push(promoted);
  if (env?.status?.lastDeployedAt) candidates.push(env.status.lastDeployedAt);
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
