import type { Revision } from "./api";
import { changedKeys } from "./previous";

export interface RolloutOutcome {
  label: string;
  tone: "ok" | "warn" | "progress";
  // When the rollout finished (ok only).
  at?: string;
}

interface EnvLike {
  metadata?: { annotations?: Record<string, string> };
  status?: { state?: string; rolloutCompletedAt?: string; [k: string]: unknown };
}

const PROMOTED_AT = "kuso.sislelabs.com/promoted-at";

// configRolloutOutcome: the pod outcome of the newest config change, so a
// config-only rollout reads as a deploy in history. Only the newest change
// gets one — older rollouts left no per-change record to read. null when
// an image promotion came after it (that row carries the outcome) or the
// change didn't roll any pods (e.g. domains).
export function configRolloutOutcome(
  revisions: readonly Revision[],
  env: EnvLike | undefined,
): { id: string; outcome: RolloutOutcome } | null {
  if (!env) return null;
  let newest: Revision | undefined;
  for (const r of revisions) {
    if (changedKeys(r) === null) continue;
    if (!newest || Date.parse(r.createdAt) > Date.parse(newest.createdAt)) newest = r;
  }
  if (!newest) return null;
  const changedAt = Date.parse(newest.createdAt);
  const promoted = Date.parse(env.metadata?.annotations?.[PROMOTED_AT] ?? "");
  if (Number.isFinite(promoted) && promoted > changedAt) return null;
  const state = env.status?.state;
  if (state === "rollout_failing") return { id: newest.id, outcome: { label: "Rollout failing", tone: "warn" } };
  if (state === "deploying") return { id: newest.id, outcome: { label: "Rolling out", tone: "progress" } };
  const done = env.status?.rolloutCompletedAt;
  const doneAt = Date.parse(done ?? "");
  // The completion stamp has second precision; allow the same second.
  if (done && Number.isFinite(doneAt) && doneAt >= changedAt - 1_000) {
    return { id: newest.id, outcome: { label: "Rolled out", tone: "ok", at: done } };
  }
  return null;
}
