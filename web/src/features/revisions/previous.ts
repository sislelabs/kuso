import type { Revision } from "./api";

// What a revision changed: the top-level keys of a PATCH snapshot
// ({patch:{healthcheck:…}}), or the op of a whole-list snapshot
// ({op:"service.envVars", envVars:[…]}). null = not replayable.
export function changedKeys(rev: Revision): string[] | null {
  const s = rev.snapshot;
  if (!s || typeof s !== "object") return null;
  const o = s as Record<string, unknown>;
  if (o.informational === true) return null;
  if (o.patch && typeof o.patch === "object") {
    const keys = Object.keys(o.patch as Record<string, unknown>).sort();
    return keys.length > 0 ? keys : null;
  }
  if (typeof o.op === "string" && o.op) return [`op:${o.op}`];
  return null;
}

const sameKeys = (a: string[], b: string[]) => a.length === b.length && a.every((k, i) => k === b[i]);

export interface RevertPlan {
  // The newest config change: the one that started the failing rollout.
  change: Revision;
  // The newest older revision that set exactly the same fields; reverting
  // it re-applies the config the previous version ran with.
  target: Revision;
}

// previousConfig finds what "Revert to previous" replays. Only a revision
// touching the same field set qualifies, so the revert can't drag along
// unrelated older settings. null when the change has no earlier
// counterpart (e.g. the first time a health check was set).
export function previousConfig(revisions: readonly Revision[]): RevertPlan | null {
  const sorted = [...revisions]
    .filter((r) => Number.isFinite(Date.parse(r.createdAt)))
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
  const idx = sorted.findIndex((r) => changedKeys(r) !== null);
  if (idx < 0) return null;
  const change = sorted[idx];
  const keys = changedKeys(change)!;
  const target = sorted.slice(idx + 1).find((r) => {
    const k = changedKeys(r);
    return k !== null && sameKeys(k, keys);
  });
  return target ? { change, target } : null;
}
