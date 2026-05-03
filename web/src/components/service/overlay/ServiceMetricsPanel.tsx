"use client";

import { useState, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cpu, MemoryStick, Activity, AlertTriangle, Timer } from "lucide-react";
import { useService } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";

const RANGES = ["1h", "6h", "1d", "7d", "30d"] as const;
type Range = (typeof RANGES)[number];

interface Props {
  project: string;
  service: string;
}

interface PodMetric {
  pod: string;
  timestamp?: string;
  cpuMillicores: number;
  memBytes: number;
}
interface EnvMetricsResponse {
  env: string;
  window?: string;
  pods: PodMetric[];
}
interface TimeseriesResponse {
  env: string;
  range: string;
  step: string;
  series: {
    requests?: [number, number][];
    errors?: [number, number][];
    p95ms?: [number, number][];
  };
}

export function ServiceMetricsPanel({ project, service }: Props) {
  const [range, setRange] = useState<Range>("1h");

  const svc = useService(project, service);
  const envs = useEnvironments(project);
  void svc;
  const fqn = project + "-" + service;
  const prodEnv = (envs.data ?? []).find(
    (e) => e.spec.service === fqn && e.spec.kind === "production"
  );
  const envName = prodEnv?.metadata.name ?? "";

  const podMetrics = useQuery({
    queryKey: ["kubernetes", "envs", envName, "metrics"],
    queryFn: () => api<EnvMetricsResponse>(`/api/kubernetes/envs/${encodeURIComponent(envName)}/metrics`),
    enabled: !!envName,
    refetchInterval: 15_000,
    staleTime: 10_000,
  });

  const traffic = useQuery({
    queryKey: ["kubernetes", "envs", envName, "timeseries", range],
    queryFn: () =>
      api<TimeseriesResponse>(
        `/api/kubernetes/envs/${encodeURIComponent(envName)}/timeseries?range=${range}`
      ),
    enabled: !!envName,
    refetchInterval: 30_000,
    staleTime: 20_000,
  });

  const totalCpu = (podMetrics.data?.pods ?? []).reduce((acc, p) => acc + p.cpuMillicores, 0);
  const totalMem = (podMetrics.data?.pods ?? []).reduce((acc, p) => acc + p.memBytes, 0);
  const podCount = (podMetrics.data?.pods ?? []).length;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Traffic & runtime</h3>
        <div className="inline-flex rounded-md border border-[var(--border-subtle)] p-0.5 text-xs">
          {RANGES.map((r) => (
            <button
              key={r}
              type="button"
              onClick={() => setRange(r)}
              className={cn(
                "px-2 py-1 rounded font-mono",
                range === r
                  ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                  : "text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
              )}
            >
              {r}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <ResourceCard
          title="CPU"
          icon={Cpu}
          primary={podCount > 0 ? formatCPU(totalCpu) : "—"}
          subtitle={
            podCount > 0
              ? `across ${podCount} pod${podCount === 1 ? "" : "s"}`
              : "no pods running"
          }
          rows={(podMetrics.data?.pods ?? []).map((p) => ({
            label: p.pod,
            value: formatCPU(p.cpuMillicores),
          }))}
        />
        <ResourceCard
          title="Memory"
          icon={MemoryStick}
          primary={podCount > 0 ? formatBytes(totalMem) : "—"}
          subtitle={
            podCount > 0
              ? `across ${podCount} pod${podCount === 1 ? "" : "s"}`
              : "no pods running"
          }
          rows={(podMetrics.data?.pods ?? []).map((p) => ({
            label: p.pod,
            value: formatBytes(p.memBytes),
          }))}
        />

        <SeriesCard
          title="Requests"
          icon={Activity}
          unit="req/s"
          points={traffic.data?.series.requests ?? []}
          loading={traffic.isPending}
        />
        <SeriesCard
          title="Error rate"
          icon={AlertTriangle}
          unit="%"
          format={(v) => (v * 100).toFixed(2)}
          points={traffic.data?.series.errors ?? []}
          loading={traffic.isPending}
          danger
        />
        <SeriesCard
          title="Response time (p95)"
          icon={Timer}
          unit="ms"
          format={(v) => v.toFixed(0)}
          points={traffic.data?.series.p95ms ?? []}
          loading={traffic.isPending}
        />
      </div>

      {!envName && (
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          No production environment yet — push to the connected branch or hit Redeploy.
        </p>
      )}
    </div>
  );
}

function ResourceCard({
  title,
  icon: Icon,
  primary,
  subtitle,
  rows,
}: {
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  primary: string;
  subtitle: string;
  rows: { label: string; value: string }[];
}) {
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <header className="flex items-center justify-between">
        <h4 className="flex items-center gap-1.5 text-sm font-medium">
          <Icon className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          {title}
        </h4>
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">live</span>
      </header>
      <div className="mt-3">
        <div className="font-mono text-2xl font-semibold tracking-tight">{primary}</div>
        <div className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">{subtitle}</div>
      </div>
      {rows.length > 1 && (
        <ul className="mt-3 space-y-0.5 border-t border-[var(--border-subtle)] pt-2">
          {rows.map((r) => (
            <li key={r.label} className="flex items-center justify-between gap-2 font-mono text-[10px]">
              <span className="truncate text-[var(--text-tertiary)]">
                {r.label.length > 24 ? "…" + r.label.slice(-24) : r.label}
              </span>
              <span className="text-[var(--text-secondary)]">{r.value}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// SeriesCard renders a tiny inline sparkline + the latest value.
// Skips axes/grid so it stays readable at the small overlay width.
function SeriesCard({
  title,
  icon: Icon,
  unit,
  points,
  loading,
  format,
  danger,
}: {
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  unit: string;
  points: [number, number][];
  loading?: boolean;
  format?: (v: number) => string;
  danger?: boolean;
}) {
  const latest = points.length > 0 ? points[points.length - 1][1] : null;
  const display = latest !== null ? (format ? format(latest) : latest.toFixed(2)) : "—";

  const path = useMemo(() => buildSparklinePath(points), [points]);

  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <header className="flex items-center justify-between">
        <h4 className="flex items-center gap-1.5 text-sm font-medium">
          <Icon className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          {title}
        </h4>
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          {loading ? "loading" : "live"}
        </span>
      </header>
      <div className="mt-3 flex items-end justify-between gap-3">
        <div>
          <div className="font-mono text-2xl font-semibold tracking-tight">
            {display}
            {latest !== null && (
              <span className="ml-1 font-mono text-[11px] text-[var(--text-tertiary)]">{unit}</span>
            )}
          </div>
          <div className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">
            {points.length > 0 ? `${points.length} points` : "no data yet"}
          </div>
        </div>
        {points.length > 1 && (
          <svg
            viewBox="0 0 100 30"
            preserveAspectRatio="none"
            className="h-10 w-32"
            aria-hidden
          >
            <path
              d={path}
              fill="none"
              stroke={danger ? "rgb(248,113,113)" : "var(--accent)"}
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
              vectorEffect="non-scaling-stroke"
            />
          </svg>
        )}
      </div>
    </div>
  );
}

function buildSparklinePath(points: [number, number][]): string {
  if (points.length < 2) return "";
  const ys = points.map((p) => p[1]);
  const min = Math.min(...ys);
  const max = Math.max(...ys);
  const span = max - min || 1;
  const w = 100;
  const h = 30;
  return points
    .map(([_, v], i) => {
      const x = (i / (points.length - 1)) * w;
      const y = h - ((v - min) / span) * h;
      return `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
}

function formatCPU(millicores: number): string {
  if (!millicores) return "0m";
  if (millicores >= 1000) return (millicores / 1000).toFixed(2) + " cores";
  return millicores + "m";
}

function formatBytes(bytes: number): string {
  if (!bytes) return "0";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let n = bytes;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return n.toFixed(n >= 100 ? 0 : 1) + " " + units[i];
}
