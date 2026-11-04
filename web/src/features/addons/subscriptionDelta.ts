import { ApiError } from "@/lib/api-client";

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

// putSubscriptionRebased reads, computes and PUTs a subscription list,
// sending the read as `expected`. The server answers 409 when the list
// changed after the read, in which case the edit is recomputed against a
// new read. A server that predates `expected` ignores it.
export async function putSubscriptionRebased(
  read: () => Promise<string[]>,
  compute: (fresh: string[]) => string[],
  put: (next: string[], expected: string[]) => Promise<void>,
  attempts = 3,
): Promise<void> {
  for (let i = 1; ; i++) {
    const fresh = await read();
    try {
      await put(compute(fresh), fresh);
      return;
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 409) || i >= attempts) throw e;
    }
  }
}
