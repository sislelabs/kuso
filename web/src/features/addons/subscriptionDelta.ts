// applySubscriptionDelta rebases a subscription edit onto a fresh read.
// Subscription endpoints replace the whole list, so sending the list this
// tab computed from its cache would drop anything another user added in
// the meantime. Only the keys this edit added/removed relative to `base`
// (what the tab showed) are applied to `fresh`. Result is sorted.
export function applySubscriptionDelta(fresh: readonly string[], base: readonly string[], wanted: readonly string[]): string[] {
  const baseSet = new Set(base);
  const wantedSet = new Set(wanted);
  const out = new Set(fresh);
  for (const k of base) if (!wantedSet.has(k)) out.delete(k);
  for (const k of wanted) if (!baseSet.has(k)) out.add(k);
  return Array.from(out).sort();
}
