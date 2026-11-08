// collapseRuns folds consecutive items with the same key into one entry
// carrying the run length, so "13 identical rows" reads as one row ×13.
// The first (newest, for a newest-first list) item represents the run.
export interface Run<T> {
  item: T;
  count: number;
  // The last item of the run (the oldest, newest-first), for "since".
  last: T;
}

export function collapseRuns<T>(items: readonly T[], key: (item: T) => string): Run<T>[] {
  const out: Run<T>[] = [];
  let prevKey: string | null = null;
  for (const item of items) {
    const k = key(item);
    const tail = out[out.length - 1];
    if (tail && k === prevKey) {
      tail.count++;
      tail.last = item;
    } else {
      out.push({ item, count: 1, last: item });
    }
    prevKey = k;
  }
  return out;
}
