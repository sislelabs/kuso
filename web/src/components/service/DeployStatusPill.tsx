import { cn } from "@/lib/utils";

export type DeployStatus =
  | "building"
  | "deploying"
  | "active"
  | "awaiting"
  | "sleeping"
  | "stopped"
  | "failed"
  | "crashed"
  | "degraded"
  | "unknown";

// Maps the server-derived env.status.state (the unified rollup from
// server-go/internal/projects/state.go) onto the existing visual
// vocabulary. null = state absent/unrecognised (old server) — callers
// fall back to their client-side heuristics.
export function deployStatusFromServerState(
  state?: string
): DeployStatus | null {
  switch (state) {
    case "running":
      return "active";
    case "degraded":
      return "degraded";
    case "crashlooping":
      return "crashed";
    case "deploying":
      return "deploying";
    case "building":
      return "building";
    case "build_failed":
      return "failed";
    // Release hook (migration) failed: the new image wasn't promoted and
    // the last green build keeps serving by design, so the service is
    // up. Warning, not the red "failed" a dead service gets.
    case "release_failed":
      return "degraded";
    case "sleeping":
      return "sleeping";
    case "stopped":
      return "stopped";
    case "no_image":
      return "awaiting";
    case "unknown":
      return "unknown";
    default:
      return null;
  }
}

// The canonical service status words (R2-4's lib/status.ts replaces this
// at merge). Internal states map onto them; the specific cause goes in
// the detail, never the label.
export const DEPLOY_STATUS_LABEL: Record<DeployStatus | "rolling", string> = {
  active: "Live",
  rolling: "Deploying",
  building: "Building",
  deploying: "Deploying",
  degraded: "Degraded",
  failed: "Failing",
  crashed: "Failing",
  sleeping: "Sleeping",
  stopped: "Stopped",
  awaiting: "Not deployed",
  unknown: "Unknown",
};

// Default detail for states whose label hides the cause.
export const DEPLOY_STATUS_DETAIL: Partial<Record<DeployStatus | "rolling", string>> = {
  rolling: "Rolling out new pods",
  failed: "Build failed",
  crashed: "Crashlooping",
  awaiting: "Waiting for the first build",
  sleeping: "Asleep until the next request",
};

const styles: Record<DeployStatus, string> = {
  // Building/deploying ride the dedicated --building hue (yellow)
  // so they're distinct from the orange accent used for hover and
  // primary highlights. Same hue as the canvas service node's
  // animated border in the same state.
  building:
    "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30 motion-safe:animate-pulse",
  deploying:
    "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30 motion-safe:animate-pulse",
  active:
    "bg-[var(--success-subtle)] text-[var(--success)] border-[var(--success)]/30",
  // Awaiting the first build — neutral/informational, NOT red. A
  // brand-new build-based service has no image yet (held at replicas=0),
  // so it's intentionally not running rather than broken.
  awaiting:
    "bg-[var(--info-subtle)] text-[var(--info)] border-[var(--info)]/30",
  sleeping:
    "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]",
  // Stopped — explicit hard stop. Slate/grey so it reads as "off" and
  // is visually distinct from sleeping (muted tertiary) and failed (red).
  stopped:
    "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--text-secondary)]/30",
  failed:
    "bg-[var(--error-subtle)] text-[var(--error)] border-[var(--error)]/30",
  crashed:
    "bg-[var(--error-subtle)] text-[var(--error)] border-[var(--error)]/30",
  // Degraded — partially up (some replicas ready). Warning hue, not
  // error red: traffic is still being served.
  degraded:
    "bg-[var(--warning-subtle)] text-[var(--warning)] border-[var(--warning)]/30",
  unknown:
    "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]",
};

export function DeployStatusPill({
  status,
  detail,
  className,
}: {
  status: DeployStatus;
  // Specific cause ("Crashlooping", "Build failed"), shown as a tooltip.
  detail?: string;
  className?: string;
}) {
  const label = DEPLOY_STATUS_LABEL[status];
  return (
    <span
      title={detail || DEPLOY_STATUS_DETAIL[status] || label}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium",
        styles[status],
        className
      )}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {label}
    </span>
  );
}
