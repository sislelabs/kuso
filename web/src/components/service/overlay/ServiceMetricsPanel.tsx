"use client";

import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { AlertTriangle, Info } from "lucide-react";
import type { KusoEnvironment } from "@/types/projects";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { TimeSeriesChart, seriesShape } from "@/components/shared/s3-TimeSeriesChart";
import type { ChartMarker, ChartThreshold, Point, SeriesShape } from "@/components/shared/s3-TimeSeriesChart";
import { Callout } from "@/components/ui/callout";
import { TimeRangePicker } from "@/components/shared/TimeRangePicker";
import { openLogsAt } from "@/components/logs/logViewStore";
import { timeRangeMs, timeRangeSpan } from "@/lib/time-range";
import { MEMORY_WARN_RATIO, memoryPressure, parseMemoryQuantity, useBuilds, useService } from "@/features/services";
import {
  mergeSamples,
  parseCpuQuantity,
  stepToMs,
  USAGE_HISTORY_MIN_VERSION,
  useEnvPodMetrics,
  useEnvTimeseries,
  useServiceUsageHistory,
} from "@/features/service-metrics";
import type { ResourceSample } from "@/features/service-metrics";
import { shortSha } from "@/lib/format";
import { cn } from "@/lib/utils";

const RANGES = ["1h", "6h", "24h", "7d", "30d"] as const;
// The server keeps CPU/memory history for this long.
const HISTORY_MAX_MS = 24 * 3_600_000;

interface Props {
  project: string;
  service: string;
  // The env CR the overlay's env-switcher resolved; undefined while envs
  // load or before the first deploy (queries stay disabled).
  env?: KusoEnvironment;
  // Per-pod memory limit (k8s quantity) from the service spec.
  memoryLimit?: string;
  // Switches the overlay to Logs; a chart click hands it the time window
  // first. Without it charts aren't clickable.
  onViewLogs?: () => void;
}

const rangeToMs = (r: string) => timeRangeMs(r) ?? 0;
const rangeLabel = timeRangeSpan;

// Shared across mounts so the range survives tab switches.
let lastRange: string = "1h";

export function ServiceMetricsPanel({ project, service, env, memoryLimit, onViewLogs }: Props) {
  const [range, setRangeState] = useState<string>(lastRange);
  const setRange = (r: string) => {
    lastRange = r;
    setRangeState(r);
  };
  const envName = env?.metadata.name ?? "";
  const pods = useEnvPodMetrics(envName);
  const traffic = useEnvTimeseries(envName, range);
  const histRange = rangeToMs(range) > HISTORY_MAX_MS ? "24h" : range;
  const history = useServiceUsageHistory(project, service, envName, histRange);
  // null (404: older server) or an error both fall back to the tab's own polls.
  const serverHist = history.isError ? null : (history.data ?? null);
  const historyMissing = history.isSuccess && history.data === null;
  const svc = useService(project, service);
  const builds = useBuilds(project, service);

  // Re-render the clock-based domain every 30s so the window slides.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(id);
  }, []);
  useEffect(() => setNow(Date.now()), [traffic.dataUpdatedAt, pods.dataUpdatedAt]);

  const rangeMs = rangeToMs(range);
  const domain: [number, number] = [now - rangeMs, now];
  const stepMs = stepToMs(traffic.data?.step ?? "") || rangeMs / 120;

  const markers: ChartMarker[] = useMemo(
    () =>
      (builds.data ?? [])
        .filter((b) => b.status === "succeeded" && b.finishedAt)
        .map((b) => ({
          t: Date.parse(b.finishedAt!),
          label: `Deploy ${b.commitSha ? shortSha(b.commitSha) : (b.ref ?? b.id)}`,
          detail: b.commitMessage?.split("\n")[0],
        }))
        .filter((m) => !Number.isNaN(m.t)),
    [builds.data],
  );

  const series = traffic.data?.series;
  const req: Point[] = useMemo(() => (series?.requests ?? []).map(([t, v]) => [t * 1000, v]), [series]);
  // Error rate is undefined where there was no traffic; the server's
  // clamp_min(…,1) turns that into 0, which would draw a reassuring
  // flat line over a dead service.
  const err: Point[] = useMemo(() => {
    const reqAt = new Map((series?.requests ?? []).map(([t, v]) => [t, v]));
    return (series?.errors ?? []).map(([t, v]) => [t * 1000, (reqAt.get(t) ?? 0) > 0 ? v * 100 : null]);
  }, [series]);
  const p95: Point[] = useMemo(() => (series?.p95ms ?? []).map(([t, v]) => [t * 1000, v]), [series]);

  const reqStats = stats(req);
  const errOverall = useMemo(() => {
    const reqAt = new Map((series?.requests ?? []).map(([t, v]) => [t, v]));
    let num = 0;
    let den = 0;
    for (const [t, v] of series?.errors ?? []) {
      const r = reqAt.get(t) ?? 0;
      num += v * r;
      den += r;
    }
    return den > 0 ? (num / den) * 100 : null;
  }, [series]);
  const p95Stats = stats(p95);
  const trafficGapMs = stepMs * 2.5;
  const errShape = seriesShape(err, trafficGapMs, domain);
  const p95Shape = seriesShape(p95, trafficGapMs, domain);
  const hasTraffic = reqStats.max !== null && reqStats.max > 0;

  const limits = svc.data?.spec.resources;
  const memLimit = parseMemoryQuantity(limits?.limits?.memory ?? memoryLimit);
  const cpuLimit = parseCpuQuantity(limits?.limits?.cpu);
  const cpuRequest = parseCpuQuantity(limits?.requests?.cpu);
  const current = pods.data?.pods ?? [];
  const podCount = current.length;
  const pressure = memoryPressure(current.map((p) => p.memBytes), memLimit);
  const memWarn = pressure !== undefined && pressure > MEMORY_WARN_RATIO;

  const resSamples = useMemo(
    () => (serverHist ? mergeSamples(serverHist.samples, pods.samples) : pods.samples),
    [serverHist, pods.samples],
  );
  const resGapMs = serverHist ? Math.max(60_000, serverHist.stepSeconds * 1000 * 2.5) : 60_000;
  const replicas = `${podCount} replica${podCount === 1 ? "" : "s"}`;
  const resNote = serverHist
    ? `last ${rangeLabel(histRange)} · ${replicas} · server-sampled every 30s`
    : `live · ${replicas} · sampled every 15s while this tab is open`;

  // One crosshair time for every chart on the tab.
  const [hoverT, setHoverT] = useState<number | null>(null);
  const deploysInRange = markers.filter((m) => m.t >= domain[0] && m.t <= domain[1]).length;

  // Logs around the picked moment: one sample step either side, at
  // least a minute, so the spike's own lines are in view.
  const pick = onViewLogs
    ? (t: number, step: number) => {
        const half = Math.max(60_000, step);
        openLogsAt(project, service, {
          q: "",
          range: { kind: "abs", from: new Date(t - half).toISOString(), to: new Date(Math.min(Date.now(), t + half)).toISOString() },
          levels: [],
          pod: "",
        });
        onViewLogs();
      }
    : undefined;
  const pickTraffic = pick ? (t: number) => pick(t, stepMs) : undefined;
  const pickHint = pick ? "Click to open logs from this moment" : undefined;

  if (!envName) {
    return (
      <EmptyPanel>
        No environment is deployed yet. Metrics appear after the first successful deploy — push to the connected branch or
        hit Redeploy.
      </EmptyPanel>
    );
  }

  const trafficDim = traffic.isPlaceholderData || (traffic.isFetching && !traffic.isPending);

  return (
    <div className="@container space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold text-[var(--text-primary)]">Metrics</h3>
          <p className="text-xs text-[var(--text-secondary)]">
            <span className="font-mono">{envName}</span>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <DeployLegend count={deploysInRange} />
          <TimeRangePicker presets={RANGES} value={range} onChange={setRange} ariaLabel="Metrics time range" />
        </div>
      </div>

      <section aria-labelledby="m-traffic" className="space-y-3">
        <SectionHeading
          id="m-traffic"
          title="Traffic"
          note={`last ${rangeLabel(range)} · from the ingress${pick ? " · click a chart to see logs from that moment" : ""}`}
        />
        {traffic.isError ? (
          <QueryErrorState what="traffic metrics" error={traffic.error} onRetry={() => void traffic.refetch()} />
        ) : (
          <div className="grid gap-3 @2xl:grid-cols-3">
            <ChartCard
              title="Requests"
              value={reqStats.avg === null ? "—" : fmtRate(reqStats.avg)}
              valueNote="avg / s"
              foot={reqStats.max !== null ? `peak ${fmtRate(reqStats.max)}/s · ≈${fmtCount((reqStats.avg ?? 0) * (rangeMs / 1000))} requests` : undefined}
              loading={traffic.isPending}
              empty={!traffic.isPending && req.length === 0 ? <NoTraffic /> : undefined}
            >
              <TimeSeriesChart
                points={req}
                domain={domain}
                format={(v) => `${fmtRate(v)}/s`}
                ariaLabel={`Requests per second, last ${rangeLabel(range)}. Average ${fmtRate(reqStats.avg ?? 0)}, peak ${fmtRate(reqStats.max ?? 0)}.`}
                gapMs={trafficGapMs}
                markers={markers}
                minYMax={0.1}
                hoverT={hoverT}
                onHoverT={setHoverT}
                onPick={pickTraffic}
                pickHint={pickHint}
                dim={trafficDim}
              />
            </ChartCard>

            <ChartCard
              title="Error rate"
              titleNote="5xx"
              value={errOverall === null ? "—" : fmtPct(errOverall)}
              valueNote={errOverall === null ? "no requests" : "of requests"}
              foot={errShape.sparse ? `${samplesLabel(errShape)} with traffic` : undefined}
              badge={
                errOverall !== null && errOverall >= 1 ? (
                  <span className="inline-flex items-center gap-1 rounded border border-[var(--error)]/40 bg-[var(--error-subtle)] px-1.5 py-0.5 text-[11px] font-medium text-[var(--error)]">
                    <AlertTriangle className="h-3 w-3" /> Elevated
                  </span>
                ) : undefined
              }
              loading={traffic.isPending}
              empty={!traffic.isPending && !hasTraffic ? <p className="text-xs text-[var(--text-secondary)]">No requests in this range, so there&apos;s no error rate to show.</p> : undefined}
            >
              <TimeSeriesChart
                points={err}
                domain={domain}
                format={fmtPct}
                ariaLabel={`5xx error rate, last ${rangeLabel(range)}. Overall ${errOverall === null ? "no data" : fmtPct(errOverall)}.`}
                gapMs={trafficGapMs}
                markers={markers}
                minYMax={1}
                hoverT={hoverT}
                onHoverT={setHoverT}
                onPick={pickTraffic}
                pickHint={pickHint}
                dim={trafficDim}
              />
            </ChartCard>

            <ChartCard
              title="Response time"
              titleNote="p95"
              value={p95Stats.last === null ? "—" : fmtMs(p95Stats.last)}
              valueNote="latest"
              foot={
                p95Stats.max !== null
                  ? [p95Shape.sparse ? samplesLabel(p95Shape) : null, `peak ${fmtMs(p95Stats.max)}`, `avg ${fmtMs(p95Stats.avg ?? 0)}`]
                      .filter(Boolean)
                      .join(" · ")
                  : undefined
              }
              loading={traffic.isPending}
              empty={!traffic.isPending && p95.length === 0 ? <p className="text-xs text-[var(--text-secondary)]">Latency is measured per request — none in this range.</p> : undefined}
            >
              <TimeSeriesChart
                points={p95}
                domain={domain}
                format={fmtMs}
                fill={false}
                ariaLabel={`95th percentile response time, last ${rangeLabel(range)}. Peak ${fmtMs(p95Stats.max ?? 0)}.`}
                gapMs={trafficGapMs}
                markers={markers}
                minYMax={10}
                hoverT={hoverT}
                onHoverT={setHoverT}
                onPick={pickTraffic}
                pickHint={pickHint}
                dim={trafficDim}
              />
            </ChartCard>
          </div>
        )}
      </section>

      <section aria-labelledby="m-res" className="space-y-3">
        <SectionHeading id="m-res" title="Resources" note={resNote} />
        {!serverHist && !pods.isError && (historyMissing || history.isError) && (podCount > 0 || resSamples.length > 0) && (
          <Callout tone="neutral" className="py-2.5 text-xs">
            {historyMissing
              ? `CPU and memory history needs kuso ${USAGE_HISTORY_MIN_VERSION} or newer. Until then this shows live samples taken since you opened this tab.`
              : "Couldn't load CPU and memory history. Showing live samples taken since you opened this tab."}
          </Callout>
        )}
        {pods.isError ? (
          <QueryErrorState what="pod metrics" error={pods.error} onRetry={() => void pods.refetch()} />
        ) : (
          <ResourceCharts
            samples={resSamples}
            now={now}
            rangeMs={rangeMs}
            gapMs={resGapMs}
            live={!serverHist}
            loading={pods.isPending || history.isPending}
            podCount={podCount}
            cpuLimit={cpuLimit}
            cpuRequest={cpuRequest}
            memLimit={memLimit}
            memWarn={memWarn}
            pressure={pressure}
            markers={markers}
            hoverT={hoverT}
            onHoverT={setHoverT}
            onPick={pick ? (t) => pick(t, resGapMs / 2.5) : undefined}
            pickHint={pickHint}
            perPod={current.map((p) => ({ pod: p.pod, cpu: p.cpuMillicores, mem: p.memBytes }))}
          />
        )}
      </section>
    </div>
  );
}

function ResourceCharts({
  samples,
  now,
  rangeMs,
  gapMs,
  live,
  loading,
  podCount,
  cpuLimit,
  cpuRequest,
  memLimit,
  memWarn,
  pressure,
  markers,
  hoverT,
  onHoverT,
  onPick,
  pickHint,
  perPod,
}: {
  onPick?: (t: number) => void;
  pickHint?: string;
  samples: ResourceSample[];
  now: number;
  rangeMs: number;
  gapMs: number;
  // Samples are this tab's own polls, not server history.
  live: boolean;
  loading: boolean;
  podCount: number;
  cpuLimit?: number;
  cpuRequest?: number;
  memLimit?: number;
  memWarn: boolean;
  pressure?: number;
  markers: ChartMarker[];
  hoverT: number | null;
  onHoverT: (t: number | null) => void;
  perPod: { pod: string; cpu: number; mem: number }[];
}) {
  const last = samples[samples.length - 1];
  // Window: just past the oldest sample (at least 5 minutes) so the
  // first few samples aren't squashed into the right edge, at most the range.
  const first = samples[0]?.t ?? now;
  const span = Math.min(rangeMs, Math.max(5 * 60_000, now - first + 60_000));
  const domain: [number, number] = [now - span, now];
  const cpu: Point[] = samples.map((s) => [s.t, s.cpuMillicores]);
  const mem: Point[] = samples.map((s) => [s.t, s.memBytes]);
  const shape = seriesShape(cpu, gapMs, domain);
  const n = Math.max(1, podCount);

  const cpuTh: ChartThreshold[] = [];
  if (cpuLimit) cpuTh.push({ value: cpuLimit * n, label: `limit ${fmtCpu(cpuLimit * n)}` });
  else if (cpuRequest) cpuTh.push({ value: cpuRequest * n, label: `request ${fmtCpu(cpuRequest * n)}` });
  const memTh: ChartThreshold[] = memLimit ? [{ value: memLimit * n, label: `limit ${fmtBytes(memLimit * n)}` }] : [];

  const collectingNote = !shape.sparse
    ? undefined
    : live
      ? `${samplesLabel(shape)} so far · a new one every 15s`
      : samplesLabel(shape);

  if (!loading && podCount === 0 && samples.length === 0) {
    return (
      <EmptyPanel>
        No replicas are running in this environment (stopped, sleeping or not yet deployed), so there&apos;s no CPU or memory
        to show.
      </EmptyPanel>
    );
  }

  return (
    <div className="grid gap-3 @lg:grid-cols-2">
      <ChartCard
        title="CPU"
        value={last ? fmtCpu(last.cpuMillicores) : "—"}
        valueNote={cpuLimit ? `of ${fmtCpu(cpuLimit * n)} limit` : "now"}
        foot={collectingNote ?? (cpuLimit || cpuRequest ? undefined : "No CPU limit set")}
        loading={loading}
        pods={perPod.length > 1 ? perPod.map((p) => ({ pod: p.pod, value: fmtCpu(p.cpu) })) : undefined}
      >
        <TimeSeriesChart
          points={cpu}
          domain={domain}
          format={fmtCpu}
          ariaLabel={`CPU usage across ${podCount} replicas, current ${last ? fmtCpu(last.cpuMillicores) : "unknown"}.`}
          gapMs={gapMs}
          thresholds={cpuTh}
          markers={markers}
          minYMax={10}
          hoverT={hoverT}
          onHoverT={onHoverT}
          onPick={onPick}
          pickHint={pickHint}
        />
      </ChartCard>
      <ChartCard
        title="Memory"
        value={last ? fmtBytes(last.memBytes) : "—"}
        valueNote={memLimit ? `of ${fmtBytes(memLimit * n)} limit` : "now"}
        warn={memWarn}
        badge={
          memWarn && pressure !== undefined ? (
            <span className="inline-flex items-center gap-1 rounded border border-[var(--warning)]/40 bg-[var(--warning-subtle)] px-1.5 py-0.5 text-[11px] font-medium text-[var(--warning)]">
              <AlertTriangle className="h-3 w-3" /> {Math.round(pressure * 100)}% of limit
            </span>
          ) : undefined
        }
        foot={collectingNote ?? (memLimit ? `peak replica at ${Math.round((pressure ?? 0) * 100)}% of its limit` : "No memory limit set")}
        loading={loading}
        pods={perPod.length > 1 ? perPod.map((p) => ({ pod: p.pod, value: fmtBytes(p.mem) })) : undefined}
      >
        <TimeSeriesChart
          points={mem}
          domain={domain}
          format={fmtBytes}
          ariaLabel={`Memory usage across ${podCount} replicas, current ${last ? fmtBytes(last.memBytes) : "unknown"}.`}
          gapMs={gapMs}
          thresholds={memTh}
          markers={markers}
          minYMax={64 * 1024 * 1024}
          tickUnit={memTickUnit(Math.max(memLimit ? memLimit * n : 0, ...samples.map((s) => s.memBytes)))}
          hoverT={hoverT}
          onHoverT={onHoverT}
          onPick={onPick}
          pickHint={pickHint}
        />
      </ChartCard>
    </div>
  );
}

// The deploy-marker key: what ◆ on the charts means, and how many fall
// in the selected range.
function DeployLegend({ count }: { count: number }) {
  return (
    <span
      className="inline-flex h-7 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] px-2 text-xs text-[var(--text-secondary)]"
      title="Each ◆ and dashed line marks a successful deploy. Hover one for its commit."
    >
      <svg aria-hidden width="10" height="10" viewBox="0 0 10 10" className="shrink-0">
        <path d="M5,0 L10,5 L5,10 L0,5 Z" fill="var(--text-primary)" />
      </svg>
      {count === 0 ? "No deploys in range" : `${count} ${count === 1 ? "deploy" : "deploys"}`}
    </span>
  );
}

function SectionHeading({ id, title, note }: { id: string; title: string; note: string }) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-2">
      <h4 id={id} className="text-[13px] font-medium text-[var(--text-primary)]">
        {title}
      </h4>
      <span className="text-xs text-[var(--text-tertiary)]">{note}</span>
    </div>
  );
}

function ChartCard({
  title,
  titleNote,
  value,
  valueNote,
  badge,
  foot,
  loading,
  empty,
  warn,
  pods,
  children,
}: {
  title: string;
  titleNote?: string;
  value: string;
  valueNote?: string;
  badge?: ReactNode;
  foot?: string;
  loading?: boolean;
  empty?: ReactNode;
  warn?: boolean;
  pods?: { pod: string; value: string }[];
  children: ReactNode;
}) {
  return (
    <figure
      className={cn(
        "min-w-0 rounded-lg border bg-[var(--bg-secondary)] p-4",
        warn ? "border-[var(--warning)]/50" : "border-[var(--border-subtle)]",
      )}
    >
      <figcaption className="mb-2">
        <div className="flex items-center justify-between gap-2">
          <span className="text-[13px] font-medium text-[var(--text-secondary)]">
            {title}
            {titleNote && <span className="ml-1 font-normal text-[var(--text-tertiary)]">{titleNote}</span>}
          </span>
          {badge}
        </div>
        <div className="mt-1 flex items-baseline gap-1.5">
          <span className="text-2xl font-semibold tracking-tight text-[var(--text-primary)]">{loading ? "…" : value}</span>
          {valueNote && !loading && <span className="text-xs text-[var(--text-tertiary)]">{valueNote}</span>}
        </div>
      </figcaption>
      {loading ? (
        <div className="h-[128px] animate-pulse rounded bg-[var(--bg-tertiary)]/50 motion-reduce:animate-none" />
      ) : empty ? (
        <div className="flex h-[128px] items-center justify-center rounded border border-dashed border-[var(--border-subtle)] px-4 text-center">
          {empty}
        </div>
      ) : (
        children
      )}
      {foot && !loading && !empty && <p className="mt-1.5 text-xs text-[var(--text-tertiary)]">{foot}</p>}
      {pods && (
        <ul className="mt-2 space-y-0.5 border-t border-[var(--border-subtle)] pt-2">
          {pods.map((p) => (
            <li key={p.pod} className="flex items-center justify-between gap-2 text-xs">
              <span className="truncate font-mono text-[var(--text-tertiary)]" title={p.pod}>
                {p.pod.split("-").slice(-2).join("-")}
              </span>
              <span className="tabular-nums text-[var(--text-secondary)]">{p.value}</span>
            </li>
          ))}
        </ul>
      )}
    </figure>
  );
}

function NoTraffic() {
  return (
    <p className="flex items-start gap-1.5 text-left text-xs text-[var(--text-secondary)]">
      <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
      <span>
        No request data. Request metrics come from the ingress, so they appear once the service has a domain and receives
        traffic.
      </span>
    </p>
  );
}

function EmptyPanel({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-[var(--border-subtle)] px-6 py-10 text-center text-sm text-[var(--text-secondary)]">
      {children}
    </div>
  );
}

function samplesLabel(shape: SeriesShape): string {
  return `${shape.samples} sample${shape.samples === 1 ? "" : "s"}`;
}

function stats(points: Point[]): { avg: number | null; max: number | null; last: number | null } {
  const vs = points.map((p) => p[1]).filter((v): v is number => v !== null && Number.isFinite(v));
  if (vs.length === 0) return { avg: null, max: null, last: null };
  return { avg: vs.reduce((a, b) => a + b, 0) / vs.length, max: Math.max(...vs), last: vs[vs.length - 1] };
}

function fmtRate(v: number): string {
  if (v === 0) return "0";
  if (v < 0.01) return "<0.01";
  if (v < 10) return v.toFixed(2).replace(/\.?0+$/, "");
  if (v < 1000) return v.toFixed(0);
  return `${(v / 1000).toFixed(1)}K`;
}

function fmtCount(n: number): string {
  if (n < 1000) return Math.round(n).toLocaleString();
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

function fmtPct(v: number): string {
  if (v === 0) return "0%";
  if (v < 0.1) return "<0.1%";
  return `${v < 10 ? v.toFixed(1).replace(/\.0$/, "") : v.toFixed(0)}%`;
}

function fmtMs(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10_000 ? 0 : 1)}s`;
  return `${Math.round(v)}ms`;
}

function fmtCpu(m: number): string {
  if (m >= 1000) return `${(m / 1000).toFixed(m >= 10_000 ? 0 : 2).replace(/\.?0+$/, "")} cores`;
  return `${Math.round(m)}m`;
}

function fmtBytes(b: number): string {
  if (!b) return "0";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let n = b;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1).replace(/\.0$/, "")} ${units[i]}`;
}

function memTickUnit(maxBytes: number): number {
  return maxBytes >= 1024 ** 3 ? 1024 ** 3 : 1024 ** 2;
}
