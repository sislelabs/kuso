// filterBranches narrows the remote's branches to what the user typed:
// names starting with the query first, then names containing it, each in
// the remote's order. Capped so a 1000-branch repo doesn't render 1000
// rows; `more` says how many matches were left out.
export function filterBranches(
  names: readonly string[],
  query: string,
  limit = 50,
): { shown: string[]; more: number } {
  const q = query.trim().toLowerCase();
  let matches: string[];
  if (!q) {
    matches = [...names];
  } else {
    const prefix: string[] = [];
    const inner: string[] = [];
    for (const n of names) {
      const l = n.toLowerCase();
      if (l.startsWith(q)) prefix.push(n);
      else if (l.includes(q)) inner.push(n);
    }
    matches = [...prefix, ...inner];
  }
  return { shown: matches.slice(0, limit), more: Math.max(0, matches.length - limit) };
}

// nextActive moves the highlighted option with the arrow keys, wrapping at
// both ends. -1 means nothing is highlighted yet.
export function nextActive(current: number, count: number, key: "ArrowDown" | "ArrowUp"): number {
  if (count === 0) return -1;
  if (key === "ArrowDown") return current < 0 ? 0 : (current + 1) % count;
  return current < 0 ? count - 1 : (current - 1 + count) % count;
}
