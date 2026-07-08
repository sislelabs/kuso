// Minimal 5-field cron → "next N run times" computer, all in UTC (kuso schedules
// are UTC, matching the picker + the rendered k8s CronJob). Supports the field
// syntax kuso's CronPicker can emit and the common hand-written forms: "*",
// lists "a,b", ranges "a-b", and steps "*/n" or "a-b/n". No external dependency
// (cron-parser would pull ~30kB for this one read-only display need).
//
// Returns [] for an unparseable expression so callers can fall back to showing
// the raw string. Bounded search: scans forward minute-by-minute up to ~366 days.

type Field = { min: number; max: number };

const FIELDS: Field[] = [
  { min: 0, max: 59 }, // minute
  { min: 0, max: 23 }, // hour
  { min: 1, max: 31 }, // day of month
  { min: 1, max: 12 }, // month
  { min: 0, max: 6 }, // day of week (0 = Sunday)
];

function parseField(spec: string, { min, max }: Field): Set<number> | null {
  const out = new Set<number>();
  for (const part of spec.split(",")) {
    const [rangePart, stepPart] = part.split("/");
    const step = stepPart ? Number(stepPart) : 1;
    if (!Number.isInteger(step) || step < 1) return null;
    let lo = min;
    let hi = max;
    if (rangePart !== "*" && rangePart !== "") {
      const bounds = rangePart.split("-");
      lo = Number(bounds[0]);
      hi = bounds.length > 1 ? Number(bounds[1]) : lo;
      if (!Number.isInteger(lo) || !Number.isInteger(hi)) return null;
      if (lo < min || hi > max || lo > hi) return null;
    }
    for (let v = lo; v <= hi; v += step) out.add(v);
  }
  return out.size ? out : null;
}

export function parseCron(
  expr: string,
): { minute: Set<number>; hour: Set<number>; dom: Set<number>; month: Set<number>; dow: Set<number>; domRestricted: boolean; dowRestricted: boolean } | null {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return null;
  const sets = parts.map((p, i) => parseField(p, FIELDS[i]));
  if (sets.some((s) => s === null)) return null;
  const [minute, hour, dom, month, dow] = sets as Set<number>[];
  return {
    minute,
    hour,
    dom,
    month,
    dow,
    // Vixie-cron semantics: when BOTH day-of-month and day-of-week are
    // restricted (not "*"), a run fires if EITHER matches. When only one is
    // restricted, only that one constrains. "*" here means "unrestricted".
    domRestricted: parts[2] !== "*",
    dowRestricted: parts[4] !== "*",
  };
}

// Next `count` fire times strictly after `from` (default now), in UTC.
export function nextRuns(expr: string, count = 3, from: Date = new Date()): Date[] {
  const c = parseCron(expr);
  if (!c) return [];
  const out: Date[] = [];
  // Start at the next whole minute after `from`.
  const t = new Date(Date.UTC(from.getUTCFullYear(), from.getUTCMonth(), from.getUTCDate(), from.getUTCHours(), from.getUTCMinutes() + 1, 0, 0));
  const limit = 366 * 24 * 60; // one year of minutes — plenty for any real schedule
  for (let i = 0; i < limit && out.length < count; i++) {
    const dom = t.getUTCDate();
    const dow = t.getUTCDay();
    const dayOk =
      c.domRestricted && c.dowRestricted
        ? c.dom.has(dom) || c.dow.has(dow)
        : (!c.domRestricted || c.dom.has(dom)) && (!c.dowRestricted || c.dow.has(dow));
    if (c.minute.has(t.getUTCMinutes()) && c.hour.has(t.getUTCHours()) && c.month.has(t.getUTCMonth() + 1) && dayOk) {
      out.push(new Date(t));
    }
    t.setUTCMinutes(t.getUTCMinutes() + 1);
  }
  return out;
}
