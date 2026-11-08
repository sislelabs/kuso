// Shared vocabulary for time-range pickers (metrics, logs, errors,
// usage). Presets are strings the server already understands ("15m",
// "24h", "7d"); never feed them to Go's time.ParseDuration directly —
// it has no day unit.

export type TimeRangePreset = "5m" | "15m" | "30m" | "1h" | "3h" | "6h" | "12h" | "24h" | "2d" | "7d" | "14d" | "30d";

export const DEFAULT_TIME_RANGE_PRESETS: readonly TimeRangePreset[] = ["15m", "1h", "6h", "24h", "7d", "30d"];

const UNIT_MS = { m: 60_000, h: 3_600_000, d: 86_400_000 } as const;
const UNIT_WORD = { m: "minute", h: "hour", d: "day" } as const;

function parse(preset: string): { n: number; unit: keyof typeof UNIT_MS } | null {
  const m = /^(\d+)([mhd])$/.exec(preset.trim());
  if (!m) return null;
  return { n: Number(m[1]), unit: m[2] as keyof typeof UNIT_MS };
}

/** Milliseconds in a preset; null when it isn't a "<n><m|h|d>" string. */
export function timeRangeMs(preset: string): number | null {
  const p = parse(preset);
  return p ? p.n * UNIT_MS[p.unit] : null;
}

/** The span in words, for prose: "24h" → "24 hours". */
export function timeRangeSpan(preset: string): string {
  const p = parse(preset);
  if (!p) return preset;
  return `${p.n} ${UNIT_WORD[p.unit]}${p.n === 1 ? "" : "s"}`;
}

/** Long form for aria/tooltips: "24h" → "Last 24 hours". */
export function timeRangeLabel(preset: string): string {
  return parse(preset) ? `Last ${timeRangeSpan(preset)}` : preset;
}
