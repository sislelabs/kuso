export interface LabelRow {
  key: string;
  value: string;
}

// placementLabels turns editor rows into the placement.labels map. A row
// with a key and a blank value is kept: it's a presence-only rule (the
// node must carry the key, any value), which placement.Matches and the
// chart's `Exists` affinity both honour.
export function placementLabels(rows: LabelRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const key = r.key.trim();
    if (key) out[key] = r.value.trim();
  }
  return out;
}

// nodeMatchesLabels mirrors placement.Matches: every rule must hold,
// blank values match on key presence.
export function nodeMatchesLabels(nodeLabels: Record<string, string>, rows: LabelRow[]): boolean {
  for (const [key, value] of Object.entries(placementLabels(rows))) {
    if (value === "") {
      if (!(key in nodeLabels)) return false;
    } else if (nodeLabels[key] !== value) {
      return false;
    }
  }
  return true;
}
