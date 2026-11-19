import type { Insights, InsightsPoint, InsightsService } from "@/features/insights";
import { formatCpu, formatLatency } from "@/lib/format";
import { deployEffect, groupDeploys, metricDef, serviceId, serviceLabel, type DeployGroup, type MetricKey } from "./insightsModel";

export type Tone = "danger" | "warning";

export interface AttentionRow {
  tone: Tone;
  /** short label, e.g. "Down", "Build failed" */
  kind: string;
  title: string;
  detail?: string;
  href?: string;
}

const MiB = 1024 * 1024;

function formatBytes(b: number): string {
  if (b >= 1024 * MiB) return `${(b / (1024 * MiB)).toFixed(1).replace(/\.0$/, "")} GiB`;
  return `${Math.round(b / MiB)} MiB`;
}

export function serviceHref(project: string, service: string): string {
  return `/projects/${encodeURIComponent(project)}?service=${encodeURIComponent(service)}`;
}

export function compareHref(id: string, metric: MetricKey): string {
  return `/insights?v=compare&m=${metric}&s=${encodeURIComponent(id)}`;
}

// liveFailedBuilds keeps each service's latest failed build, unless a
// production deploy of that service succeeded after it.
export function liveFailedBuilds(data: Pick<Insights, "failedBuilds" | "deploys">): Insights["failedBuilds"] {
  const lastOk = new Map<string, number>();
  for (const d of data.deploys) {
    const id = serviceId(d);
    lastOk.set(id, Math.max(lastOk.get(id) ?? 0, Date.parse(d.at)));
  }
  const seen = new Set<string>();
  const out: Insights["failedBuilds"] = [];
  for (const b of [...data.failedBuilds].sort((x, y) => Date.parse(y.at) - Date.parse(x.at))) {
    const id = serviceId(b);
    if (seen.has(id)) continue;
    seen.add(id);
    if ((lastOk.get(id) ?? 0) > Date.parse(b.at)) continue;
    out.push(b);
  }
  return out;
}

function avg(points: readonly InsightsPoint[]): number | null {
  if (!points.length) return null;
  return points.reduce((n, [, v]) => n + v, 0) / points.length;
}

function median(values: number[]): number | null {
  if (!values.length) return null;
  const s = [...values].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

function lastHour(points: readonly InsightsPoint[], until: number): InsightsPoint[] {
  return points.filter(([t]) => t > until - 3600);
}

function between(points: readonly InsightsPoint[], lo: number, hi: number): InsightsPoint[] {
  return points.filter(([t]) => t >= lo && t < hi);
}

// Latency and error rates on a trickle of requests are mostly noise (a
// cold start after a deploy reads as a 5x slowdown), so those checks
// need this many requests a minute behind them.
const MIN_RPM = 1;

function busy(s: InsightsService, lo: number, hi: number): boolean {
  return (avg(between(s.requests, lo, hi)) ?? 0) >= MIN_RPM;
}

export interface Outlier {
  id: string;
  label: string;
  metric: MetricKey;
  tone: Tone;
  kind: string;
  detail: string;
}

// Enough usage history to judge a trend or a reservation.
const MIN_USAGE_SPAN = 6 * 3600;

function span(points: readonly InsightsPoint[]): number {
  return points.length > 1 ? points[points.length - 1][0] - points[0][0] : 0;
}

// memoryGrowth is the rise from the first sixth of the series to the
// last, when every sixth in between is at least as high as the one
// before (within 3%): a steady climb, not a single spike.
export function memoryGrowth(points: readonly InsightsPoint[]): number | null {
  if (span(points) < MIN_USAGE_SPAN || points.length < 12) return null;
  const n = 6;
  const size = Math.floor(points.length / n);
  const chunks: number[] = [];
  for (let i = 0; i < n; i++) chunks.push(avg(points.slice(i * size, i === n - 1 ? points.length : (i + 1) * size)) ?? 0);
  for (let i = 1; i < n; i++) if (chunks[i] < chunks[i - 1] * 0.97) return null;
  if (chunks[0] <= 0) return null;
  return (chunks[n - 1] - chunks[0]) / chunks[0];
}

// findOutliers flags services that are behaving unlike their own last
// 24h, close to their limits, or worse since their last deploy.
export function findOutliers(data: Insights): Outlier[] {
  const until = Date.parse(data.until) / 1000;
  const rangeSeconds = until - Date.parse(data.since) / 1000;
  const lastDeploy = new Map<string, number>();
  for (const d of data.deploys) lastDeploy.set(serviceId(d), Math.max(lastDeploy.get(serviceId(d)) ?? 0, Date.parse(d.at) / 1000));
  const out: Outlier[] = [];
  for (const s of data.services) {
    const id = serviceId(s);
    const label = serviceLabel(s);
    const add = (o: Omit<Outlier, "id" | "label">) => out.push({ id, label, ...o });

    const busyNow = busy(s, until - 3600, until + 1);
    const at = lastDeploy.get(id);
    const window = Math.min(3600, rangeSeconds / 6) + data.stepSeconds;
    const lat = at && busy(s, at - window, at) && busy(s, at, at + window) ? deployEffect(s.p95, at, rangeSeconds, data.stepSeconds) : null;
    const slowerSinceDeploy = !!lat && lat.pct >= 100 && lat.after >= 300;

    const p95Recent = avg(lastHour(s.p95, until));
    const p95Base = median(s.p95.map(([, v]) => v));
    if (!slowerSinceDeploy && busyNow && p95Recent !== null && p95Base !== null && p95Recent >= 500 && p95Recent >= p95Base * 3) {
      add({ metric: "p95", tone: "warning", kind: "Slow", detail: `p95 ${formatLatency(p95Recent)} in the last hour, usually ${formatLatency(p95Base)}` });
    }
    if (slowerSinceDeploy && lat) {
      add({ metric: "p95", tone: "warning", kind: "Slower since deploy", detail: `p95 ${formatLatency(lat.before)} → ${formatLatency(lat.after)}` });
    }

    const errRecent = avg(lastHour(s.errorRate, until));
    const errBase = avg(s.errorRate.filter(([t]) => t <= until - 3600));
    if (busyNow && errRecent !== null && errRecent >= 1 && errRecent >= (errBase ?? 0) * 2) {
      add({ metric: "errorRate", tone: errRecent >= 5 ? "danger" : "warning", kind: "Errors", detail: `${errRecent.toFixed(1)}% 5xx in the last hour, usually ${(errBase ?? 0).toFixed(1)}%` });
    }

    const growth = memoryGrowth(s.memory);
    const memNow = s.memory.at(-1)?.[1] ?? 0;
    if (growth !== null && growth >= 0.25 && memNow >= 100 * MiB) {
      add({ metric: "memory", tone: "warning", kind: "Memory climbing", detail: `up ${Math.round(growth * 100)}% over ${Math.round(span(s.memory) / 3600)}h, now ${formatBytes(memNow)}` });
    }

    const pods = Math.max(1, s.pods);
    const memLimit = s.resources?.memLimitBytes;
    if (memLimit && memNow / pods >= memLimit * 0.85) {
      add({ metric: "memory", tone: memNow / pods >= memLimit * 0.95 ? "danger" : "warning", kind: "Near memory limit", detail: `${formatBytes(memNow / pods)} of ${formatBytes(memLimit)} per pod` });
    }
    const cpuLimit = s.resources?.cpuLimitMilli;
    const cpuRecent = avg(lastHour(s.cpu, until));
    if (cpuLimit && cpuRecent !== null && cpuRecent / pods >= cpuLimit * 0.85) {
      add({ metric: "cpu", tone: "warning", kind: "Near CPU limit", detail: `${formatCpu(cpuRecent / pods)} of ${formatCpu(cpuLimit)} per pod` });
    }

    if (at) {
      const mem = deployEffect(s.memory, at, rangeSeconds, data.stepSeconds);
      if (mem && mem.pct >= 40 && mem.after >= 100 * MiB) {
        add({ metric: "memory", tone: "warning", kind: "Heavier since deploy", detail: `memory ${formatBytes(mem.before)} → ${formatBytes(mem.after)}` });
      }
    }
  }
  return out.sort((a, b) => (a.tone === b.tone ? a.label.localeCompare(b.label) : a.tone === "danger" ? -1 : 1));
}

export interface WasteRow {
  id: string;
  label: string;
  kind: "Idle" | "Memory reserved" | "CPU reserved";
  detail: string;
}

// findWaste flags public services nobody called in the range and
// reservations far above what the service uses. publicIds are services
// with a host, so workers without traffic aren't called idle.
export function findWaste(data: Insights, publicIds: ReadonlySet<string>): WasteRow[] {
  const out: WasteRow[] = [];
  for (const s of data.services) {
    const id = serviceId(s);
    const label = serviceLabel(s);
    const pods = s.pods;
    if (data.requestsAvailable && publicIds.has(id) && pods > 0 && s.requests.reduce((n, [, v]) => n + v, 0) === 0) {
      out.push({
        id,
        label,
        kind: "Idle",
        detail: `nothing came in through its URL in ${data.range}, ${pods} ${pods === 1 ? "pod" : "pods"} running. If nothing calls it inside the cluster either, it could sleep`,
      });
    }
    if (pods === 0 || span(s.memory) < MIN_USAGE_SPAN) continue;
    const memReq = (s.resources?.memRequestBytes ?? 0) * pods;
    const memPeak = Math.max(0, ...s.memory.map(([, v]) => v));
    if (memReq >= 256 * MiB && memPeak <= memReq * 0.3) {
      out.push({ id, label, kind: "Memory reserved", detail: `reserves ${formatBytes(memReq)}, peaks at ${formatBytes(memPeak)}` });
    }
    const cpuReq = (s.resources?.cpuRequestMilli ?? 0) * pods;
    const cpuAvg = avg(s.cpu) ?? 0;
    if (cpuReq >= 200 && cpuAvg <= cpuReq * 0.1) {
      out.push({ id, label, kind: "CPU reserved", detail: `reserves ${formatCpu(cpuReq)}, averages ${formatCpu(cpuAvg)}` });
    }
  }
  return out;
}

export interface ChangeRow extends DeployGroup {
  ids: string[];
  effects: Partial<Record<MetricKey, number>>;
}

const CHANGE_METRICS: MetricKey[] = ["memory", "p95", "errorRate"];

// recentChanges is each push with how memory, latency and 5xx moved
// across its services: the summed before/after windows of deployEffect.
export function recentChanges(data: Insights, limit = 12): ChangeRow[] {
  const byId = new Map<string, InsightsService>(data.services.map((s) => [serviceId(s), s]));
  const rangeSeconds = (Date.parse(data.until) - Date.parse(data.since)) / 1000;
  return groupDeploys(data.deploys)
    .slice(0, limit)
    .map((g) => {
      const ids = g.services.map((svc) => `${g.project}/${svc}`).filter((id) => byId.has(id));
      const effects: Partial<Record<MetricKey, number>> = {};
      const window = Math.min(3600, rangeSeconds / 6) + data.stepSeconds;
      for (const m of CHANGE_METRICS) {
        let before = 0;
        let after = 0;
        let ok = ids.length > 0;
        for (const id of ids) {
          // Traffic metrics only count with traffic on both sides.
          if (m !== "memory" && !(busy(byId.get(id)!, g.at - window, g.at) && busy(byId.get(id)!, g.at, g.at + window))) {
            ok = false;
            break;
          }
          const e = deployEffect(byId.get(id)![m], g.at, rangeSeconds, data.stepSeconds);
          if (!e) {
            ok = false;
            break;
          }
          before += e.before;
          after += e.after;
        }
        // 5xx is a percentage, so a move from 0 is "new errors", not ∞%.
        if (ok && before > 0) effects[m] = ((after - before) / before) * 100;
      }
      return { ...g, ids, effects };
    });
}

export function metricLabel(m: MetricKey): string {
  return metricDef(m).label;
}
