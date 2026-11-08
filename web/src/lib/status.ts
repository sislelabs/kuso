// The one status vocabulary. Every surface that names the state of a
// service, addon or job (canvas, list view, overlay headers, backups,
// health) takes its label and tone from here, so the same thing is never
// "Live" in one place and "Online" / "Healthy" / "ok" in another.

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
    default:
      return undefined;
  }
}

export type AddonStatus = "live" | "provisioning" | "degraded" | "failing" | "external" | "unknown";

export const ADDON_STATUS: Record<AddonStatus, StatusMeta> = {
  live: { label: "Live", tone: "success" },
  provisioning: { label: "Provisioning", tone: "progress", pulse: true },
  degraded: { label: "Degraded", tone: "warning" },
  failing: { label: "Failing", tone: "danger" },
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
