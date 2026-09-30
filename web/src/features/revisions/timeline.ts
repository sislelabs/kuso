import type { Revision } from "./api";

export type TimelineItem<B> =
  | { type: "build"; build: B }
  | { type: "revision"; revision: Revision };

function ts(s: string | undefined): number {
  const t = Date.parse(s ?? "");
  return Number.isFinite(t) ? t : NaN;
}

// interleaveTimeline merges newest-first builds with revisions by time.
// Build order is preserved as given; a build with no timestamp (queued)
// stays where it is and never pulls revisions ahead of it.
export function interleaveTimeline<B extends { startedAt?: string; finishedAt?: string }>(
  builds: B[],
  revisions: Revision[],
): TimelineItem<B>[] {
  const revs = [...revisions]
    .filter((r) => Number.isFinite(ts(r.createdAt)))
    .sort((a, b) => ts(b.createdAt) - ts(a.createdAt));
  const out: TimelineItem<B>[] = [];
  let ri = 0;
  for (const build of builds) {
    const bt = ts(build.startedAt ?? build.finishedAt);
    if (Number.isFinite(bt)) {
      while (ri < revs.length && ts(revs[ri].createdAt) > bt) {
        out.push({ type: "revision", revision: revs[ri++] });
      }
    }
    out.push({ type: "build", build });
  }
  while (ri < revs.length) out.push({ type: "revision", revision: revs[ri++] });
  return out;
}
