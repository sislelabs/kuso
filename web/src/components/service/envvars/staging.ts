import type { StagedEnv } from "@/features/staged";
import { rid, type Row } from "@/components/service/envVarTransforms";
import { isSensitiveVar } from "./sensitivity";

interface Plan {
  valueWrites: { name: string; value: string }[];
  deletes: string[];
}

// The deployed value as the editor knows it; undefined when it's held off
// the CR and wasn't revealed (secret-backed, blank on read).
function knownValue(b: Row | undefined): string | undefined {
  if (!b) return undefined;
  if ((b.secretBacked || b.fromSecret) && b.value === "") return undefined;
  return b.value;
}

// toStagedEnv turns the editor's plan into the staged form, keeping each
// variable's deployed value for the review.
export function toStagedEnv(plan: Plan, baseline: readonly Row[], rows: readonly Row[]): StagedEnv | undefined {
  const byName = new Map(baseline.map((b) => [b.name.trim(), b]));
  const rowByName = new Map(rows.map((r) => [r.name.trim(), r]));
  const set = plan.valueWrites.map((w) => {
    const b = byName.get(w.name);
    const r = rowByName.get(w.name);
    return {
      name: w.name,
      value: w.value,
      before: b ? knownValue(b) : null,
      sensitive: isSensitiveVar(w.name, w.value) || !!b?.secretBacked || !!r?.secretBacked,
    };
  });
  const unset = plan.deletes.map((name) => {
    const b = byName.get(name);
    return { name, before: knownValue(b), sensitive: !!b && (b.secretBacked || isSensitiveVar(name, b.value)) };
  });
  if (set.length === 0 && unset.length === 0) return undefined;
  return { set, unset };
}

export function sameStagedEnv(a: StagedEnv | undefined, b: StagedEnv | undefined): boolean {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}

// applyStagedEnv lays staged edits over the deployed rows, sorted like the
// editor's own load.
export function applyStagedEnv(baseline: readonly Row[], staged: StagedEnv | undefined): Row[] {
  if (!staged) return [...baseline];
  const removed = new Set(staged.unset.map((u) => u.name));
  const out = baseline.filter((r) => !removed.has(r.name.trim()));
  for (const s of staged.set) {
    const i = out.findIndex((r) => r.name.trim() === s.name);
    if (i >= 0) {
      out[i] = { ...out[i], value: s.value, fromSecret: false, origValueFrom: undefined, visible: true };
    } else {
      out.push({ id: rid(), name: s.name, value: s.value, fromSecret: false, secretBacked: false, visible: true });
    }
  }
  return out.sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
}
