// Mirrors projects.PodEvent.
export interface ServicePodEvent {
  type: string; // "Normal" | "Warning"
  reason: string;
  // Raw kube text.
  message: string;
  // message in plain words; only set where the raw text is unreadable
  // (FailedScheduling).
  summary?: string;
  object: string; // "pod/<name>" or "deployment/<name>"
  count: number;
  firstSeen?: string;
  lastSeen?: string;
}

const t = (s?: string) => (s ? Date.parse(s) : NaN);

// collapseEvents merges events that read the same to a person (same type,
// reason, object and wording; kube keeps separate records per raw message
// variant) into one row with the summed count and the widest time range.
// Order (newest first) follows each group's first appearance.
export function collapseEvents(events: readonly ServicePodEvent[]): ServicePodEvent[] {
  const out: ServicePodEvent[] = [];
  const byKey = new Map<string, ServicePodEvent>();
  for (const e of events) {
    const key = [e.type, e.reason, e.object, (e.summary || e.message).trim()].join("\u0000");
    const seen = byKey.get(key);
    if (!seen) {
      const copy = { ...e, count: Math.max(1, e.count) };
      byKey.set(key, copy);
      out.push(copy);
      continue;
    }
    seen.count += Math.max(1, e.count);
    if (!(t(seen.firstSeen) <= t(e.firstSeen))) seen.firstSeen = e.firstSeen ?? seen.firstSeen;
    if (!(t(seen.lastSeen) >= t(e.lastSeen))) seen.lastSeen = e.lastSeen ?? seen.lastSeen;
  }
  return out;
}
