import type { InsightsDeploy, InsightsPoint, InsightsRange, InsightsService } from "@/features/insights";
import { formatCpu, formatLatency } from "@/lib/format";

export type MetricKey = "requests" | "cpu" | "memory" | "p95" | "errorRate";

export interface MetricDef {
  key: MetricKey;
  label: string;
  /** a value with its unit, for tooltips and the table */
  format: (v: number) => string;
  /** shorter, for axis ticks */
  tick: (v: number) => string;
  source: "traffic" | "usage";
  /** Higher is worse, so a rise after a deploy reads as a regression. */
  higherIsWorse: boolean;
  /** Axis steps are round multiples of this (1 MiB for memory). */
  tickUnit?: number;
}

const MiB = 1024 * 1024;

function formatMemory(b: number): string {
  if (b >= 1024 * MiB) return `${(b / (1024 * MiB)).toFixed(b >= 10 * 1024 * MiB ? 0 : 1)} GiB`;
  return `${Math.round(b / MiB)} MiB`;
}

function formatRate(v: number): string {
  if (v >= 10_000) return `${(v / 1000).toFixed(0)}k/min`;
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k/min`;
  if (v >= 10) return `${Math.round(v)}/min`;
  return `${v.toFixed(1).replace(/\.0$/, "")}/min`;
}

function formatPercent(v: number): string {
  if (v === 0) return "0%";
  if (v < 0.1) return "<0.1%";
  return `${v.toFixed(v < 10 ? 1 : 0)}%`;
}

export const METRICS: readonly MetricDef[] = [
  { key: "requests", label: "Requests", format: formatRate, tick: formatRate, source: "traffic", higherIsWorse: false },
  { key: "cpu", label: "CPU", format: formatCpu, tick: formatCpu, source: "usage", higherIsWorse: true },
  { key: "memory", label: "Memory", format: formatMemory, tick: formatMemory, source: "usage", higherIsWorse: true, tickUnit: MiB },
  { key: "p95", label: "p95 latency", format: formatLatency, tick: formatLatency, source: "traffic", higherIsWorse: true },
  { key: "errorRate", label: "5xx rate", format: formatPercent, tick: formatPercent, source: "traffic", higherIsWorse: true },
];

export function metricDef(key: MetricKey): MetricDef {
  return METRICS.find((m) => m.key === key) ?? METRICS[0];
}

export const RANGES: readonly InsightsRange[] = ["1h", "6h", "24h"];

/** Five chart tokens, so five services at once. */
export const SERIES_COLORS = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)", "var(--chart-4)", "var(--chart-5)"] as const;
export const MAX_SELECTED = SERIES_COLORS.length;

export function serviceId(s: Pick<InsightsService, "project" | "service">): string {
  return `${s.project}/${s.service}`;
}

export function serviceLabel(s: Pick<InsightsService, "project" | "service">): string {
  return s.project === s.service ? s.project : `${s.project} / ${s.service}`;
}

export interface SeriesStats {
  now: number;
  avg: number;
  peak: number;
}

export function seriesStats(points: readonly InsightsPoint[]): SeriesStats | null {
  if (points.length === 0) return null;
  let sum = 0;
  let peak = -Infinity;
  for (const [, v] of points) {
    sum += v;
    if (v > peak) peak = v;
  }
  return { now: points[points.length - 1][1], avg: sum / points.length, peak };
}

function avgBetween(points: readonly InsightsPoint[], lo: number, hi: number): number | null {
  let sum = 0;
  let n = 0;
  for (const [t, v] of points) {
    if (t >= lo && t < hi) {
      sum += v;
      n++;
    }
  }
  return n ? sum / n : null;
}

export interface DeployEffect {
  before: number;
  after: number;
  /** percent change, after vs before */
  pct: number;
}

// deployEffect compares the average over a window before a deploy with
// the same-length window after it, skipping one step either side so the
// rollout itself doesn't count. The window is a sixth of the range, at
// most an hour.
export function deployEffect(
  points: readonly InsightsPoint[],
  at: number,
  rangeSeconds: number,
  stepSeconds: number,
): DeployEffect | null {
  const w = Math.min(3600, rangeSeconds / 6);
  const before = avgBetween(points, at - w - stepSeconds, at - stepSeconds);
  const after = avgBetween(points, at + stepSeconds, at + w + stepSeconds);
  if (before === null || after === null || before === 0) return null;
  return { before, after, pct: ((after - before) / before) * 100 };
}

export interface DeployGroup {
  project: string;
  services: string[];
  at: number;
  commitSha: string;
  commitMessage: string;
}

// groupDeploys folds one push's per-service builds into one row: same
// project and commit, newest first.
export function groupDeploys(deploys: readonly InsightsDeploy[]): DeployGroup[] {
  const byKey = new Map<string, DeployGroup>();
  for (const d of deploys) {
    const at = Date.parse(d.at) / 1000;
    const key = `${d.project}|${d.commitSha || `${d.commitMessage}|${Math.round(at / 300)}`}`;
    const g = byKey.get(key);
    if (g) {
      if (!g.services.includes(d.service)) g.services.push(d.service);
      g.at = Math.max(g.at, at);
    } else {
      byKey.set(key, { project: d.project, services: [d.service], at, commitSha: d.commitSha, commitMessage: d.commitMessage });
    }
  }
  const out = [...byKey.values()];
  for (const g of out) g.services.sort();
  return out.sort((a, b) => b.at - a.at);
}

export function pointsFor(s: InsightsService, metric: MetricKey): InsightsPoint[] {
  return s[metric];
}

function total(points: readonly InsightsPoint[]): number {
  return points.reduce((n, [, v]) => n + v, 0);
}

export interface Preset {
  label: string;
  metric: MetricKey | null;
  pick: (services: readonly InsightsService[], deploys: readonly InsightsDeploy[]) => string[];
}

const top = (services: readonly InsightsService[], score: (s: InsightsService) => number, n = 4) =>
  [...services]
    .map((s) => ({ id: serviceId(s), v: score(s) }))
    .filter((x) => x.v > 0)
    .sort((a, b) => b.v - a.v)
    .slice(0, n)
    .map((x) => x.id);

export const PRESETS: readonly Preset[] = [
  { label: "Most traffic", metric: "requests", pick: (s) => top(s, (x) => total(x.requests)) },
  { label: "Heaviest", metric: "memory", pick: (s) => top(s, (x) => x.memory.at(-1)?.[1] ?? 0) },
  { label: "Busiest CPU", metric: "cpu", pick: (s) => top(s, (x) => seriesStats(x.cpu)?.peak ?? 0) },
  {
    label: "Deployed in range",
    metric: null,
    pick: (s, d) => {
      const ids = new Set(s.map(serviceId));
      return [...new Set(d.map(serviceId))].filter((id) => ids.has(id)).slice(0, MAX_SELECTED);
    },
  },
];

export function defaultSelection(services: readonly InsightsService[]): string[] {
  const busy = top(services, (x) => total(x.requests), 3);
  return busy.length ? busy : services.slice(0, 3).map(serviceId);
}
