import type { NodeStatus, NodeStatusTone } from "@/features/services";

// Most urgent first: broken, then needs-a-look, in flight, waiting, fine, idle.
const TONE_RANK: Record<NodeStatusTone, number> = {
  error: 0,
  warning: 1,
  progress: 2,
  info: 3,
  success: 4,
  muted: 5,
};

// sortBySeverity orders rows by status severity, then name, so the list
// doesn't reshuffle between polls and problems sit at the top.
export function sortBySeverity<T extends { status: Pick<NodeStatus, "tone">; name: string }>(rows: readonly T[]): T[] {
  return [...rows].sort(
    (a, b) => TONE_RANK[a.status.tone] - TONE_RANK[b.status.tone] || a.name.localeCompare(b.name),
  );
}
