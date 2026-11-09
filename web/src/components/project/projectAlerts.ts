// The mobile project view's alerts feed: fetch + grouping, kept free of
// React so it's cheap to test.

// Minimal feed shape — mirrors the fields TopNav's bell popover reads.
// Kept local so the incident view doesn't couple to TopNav internals.
export interface FeedEvent {
  id: number;
  type: string;
  title: string;
  body?: string;
  severity?: string;
  project?: string;
  service?: string;
  createdAt: string;
  classification?: { kind?: string; summary?: string };
}

// fetchProjectAlerts asks the scoped feed for this project's events.
// Servers that know ?project= filter server-side; older ones ignore it
// and return every project's events, so the client filters too and, when
// the first page was mostly other projects, asks once for a deeper page.
const ALERTS_PAGE = 30;
const ALERTS_DEEP = 200;
export async function fetchProjectAlerts(
  project: string,
  get: (path: string) => Promise<FeedEvent[]>,
): Promise<FeedEvent[]> {
  const path = (limit: number) =>
    `/api/notifications/my-feed?limit=${limit}&project=${encodeURIComponent(project)}`;
  const first = (await get(path(ALERTS_PAGE))) ?? [];
  const mine = first.filter((e) => e.project === project);
  const serverFiltered = mine.length === first.length;
  if (serverFiltered || first.length < ALERTS_PAGE || mine.length >= 6) return mine;
  const deep = (await get(path(ALERTS_DEEP))) ?? [];
  return deep.filter((e) => e.project === project);
}

export function groupRepeats<E extends { type: string; title: string; service?: string }>(
  events: readonly E[],
): { event: E; count: number }[] {
  const byKey = new Map<string, { event: E; count: number }>();
  for (const e of events) {
    const k = [e.type, e.service ?? "", e.title].join("\u0000");
    const g = byKey.get(k);
    if (g) g.count++;
    else byKey.set(k, { event: e, count: 1 });
  }
  return [...byKey.values()];
}
