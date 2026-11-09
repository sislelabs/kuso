import type { NodeStatus, NodeStatusKind, NodeStatusTone } from "@/features/services";
import type { ServiceStatus, StatusMeta, StatusTone } from "@/lib/status";

// Adapters from the one selector's answer (deriveServiceStatus) to the
// two shapes the surfaces render: a pill/strip StatusMeta, and the
// ServiceStatus the project cards count by. No status logic lives here.

const TONE: Record<NodeStatusTone, StatusTone> = {
  success: "success",
  warning: "warning",
  error: "danger",
  progress: "progress",
  info: "info",
  muted: "neutral",
};

export function nodeStatusMeta(s: NodeStatus): StatusMeta {
  const tone = TONE[s.tone];
  return tone === "progress" ? { label: s.label, tone, pulse: true } : { label: s.label, tone };
}

const KIND_TO_STATUS: Partial<Record<NodeStatusKind, ServiceStatus>> = {
  live: "live",
  degraded: "degraded",
  building: "building",
  deploying: "deploying",
  build_failed: "failing",
  crashing: "failing",
  stuck: "stuck",
  sleeping: "sleeping",
  stopped: "stopped",
  not_deployed: "not_deployed",
  provisioning: "deploying",
  paused: "stopped",
  unknown: "unknown",
};

const TONE_TO_STATUS: Record<NodeStatusTone, ServiceStatus> = {
  success: "live",
  warning: "degraded",
  error: "failing",
  progress: "deploying",
  info: "not_deployed",
  muted: "unknown",
};

// nodeServiceStatus buckets a selector answer for counting. A kind this
// table doesn't know yet (the selector grew a state) lands by its tone,
// so it is never counted as healthy by accident.
export function nodeServiceStatus(s: NodeStatus): ServiceStatus {
  return KIND_TO_STATUS[s.kind] ?? TONE_TO_STATUS[s.tone];
}

// Short names for the problems the header strip folds in.
export function isProblemStatus(s: NodeStatus): boolean {
  return s.tone === "error" || s.tone === "warning";
}
