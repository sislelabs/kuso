import type { Revision } from "@/features/revisions";

export interface FieldChange {
  field: string;
  // undefined = no earlier revision in the loaded history set this field.
  before?: unknown;
  after: unknown;
}

const META_KEYS = new Set(["op", "informational", "service", "env", "from", "keys"]);

// changedFields flattens what one revision wrote into dotted field paths.
// A settings save stores its PATCH body as {"patch": {...}}; other ops
// store the new value under a named key. Arrays are compared whole.
export function changedFields(snapshot: unknown): Map<string, unknown> {
  const out = new Map<string, unknown>();
  if (!snapshot || typeof snapshot !== "object") return out;
  const s = snapshot as Record<string, unknown>;
  const root = s.patch && typeof s.patch === "object" ? (s.patch as Record<string, unknown>) : s;
  const walk = (obj: Record<string, unknown>, prefix: string) => {
    for (const [k, v] of Object.entries(obj)) {
      if (!prefix && root === s && META_KEYS.has(k)) continue;
      if (v === undefined) continue;
      const path = prefix ? `${prefix}.${k}` : k;
      if (v && typeof v === "object" && !Array.isArray(v) && Object.keys(v).length > 0) {
        walk(v as Record<string, unknown>, path);
      } else {
        out.set(path, v);
      }
    }
  };
  walk(root, "");
  return out;
}

// revisionFieldChanges pairs each field a revision wrote with the value
// the newest older revision wrote to the same field. Revisions only
// store what they set, so "before" is unknown when nothing older in the
// loaded history touched that field.
export function revisionFieldChanges(rev: Revision, history: readonly Revision[]): FieldChange[] {
  const after = changedFields(rev.snapshot);
  const older = history
    .filter((r) => r.id !== rev.id && r.kind === rev.kind && r.name === rev.name && r.createdAt < rev.createdAt)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map((r) => changedFields(r.snapshot));
  const out: FieldChange[] = [];
  for (const [field, value] of after) {
    const prev = older.find((m) => m.has(field));
    out.push({ field, before: prev?.get(field), after: value });
  }
  return out;
}

const MASK = /^\*+$|^•+$|REDACTED|<masked>/i;

// formatValue renders a stored value for the diff: strings as-is, empty
// as "empty", objects as compact JSON.
export function formatValue(v: unknown): string {
  if (v === undefined) return "unknown";
  if (v === null || v === "") return "empty";
  if (typeof v === "string") return MASK.test(v) ? "hidden" : v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  if (Array.isArray(v) && v.length === 0) return "none";
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}
