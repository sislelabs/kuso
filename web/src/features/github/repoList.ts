// Ordering + owner grouping for the repo picker. The server sorts by
// last push since pushedAt existed; older servers send no pushedAt, so
// their order is kept as-is.

interface HasRepo {
  repo: { fullName: string; pushedAt?: string };
}

function pushedMs(p?: string): number | null {
  if (!p) return null;
  const t = new Date(p).getTime();
  return Number.isNaN(t) ? null : t;
}

// sortReposByPush puts the most recently pushed repo first across all
// installations. Repos without a push time keep their relative order
// after the dated ones; with no push times at all the input order is
// returned untouched.
export function sortReposByPush<T extends HasRepo>(list: readonly T[]): T[] {
  if (!list.some((e) => pushedMs(e.repo.pushedAt) !== null)) return [...list];
  return [...list].sort((a, b) => {
    const ta = pushedMs(a.repo.pushedAt);
    const tb = pushedMs(b.repo.pushedAt);
    if (ta === null && tb === null) return 0;
    if (ta === null) return 1;
    if (tb === null) return -1;
    return tb - ta;
  });
}

export function repoOwner(fullName: string): string {
  const i = fullName.indexOf("/");
  return i > 0 ? fullName.slice(0, i) : fullName;
}

// repoOwners lists the distinct owners in list order, so after
// sortReposByPush the most recently active owner comes first.
export function repoOwners(list: readonly HasRepo[]): string[] {
  const seen = new Set<string>();
  for (const e of list) seen.add(repoOwner(e.repo.fullName));
  return [...seen];
}
