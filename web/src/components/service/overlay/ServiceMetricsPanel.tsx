"use client";

import { useState } from "react";
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

export function ServiceMetricsPanel({ project, service }: Props) {
  const [range, setRange] = useState<Range>("1h");
  void range;

  // Resolve the production env CR name for this service so we can hit
  // the /api/kubernetes/envs/{env}/metrics endpoint. The env name
  // pattern is <project>-<service>-production.
  const svc = useService(project, service);
  const envs = useEnvironments(project);
  void svc;
  const fqn = project + "-" + service;
  const prodEnv = (envs.data ?? []).find(
    (e) => e.spec.service === fqn && e.spec.kind === "production"
  );
  const envName = prodEnv?.metadata.name ?? "";

  const metrics = useQuery({
    queryKey: ["kubernetes", "envs", envName, "metrics"],
    queryFn: () => api<EnvMetricsResponse>(`/api/kubernetes/envs/${encodeURIComponent(envName)}/metrics`),
    enabled: !!envName,
    refetchInterval: 15_000,
    staleTime: 10_000,
  });

  const totalCpu = (metrics.data?.pods ?? []).reduce((acc, p) => acc + p.cpuMillicores, 0);
  const totalMem = (metrics.data?.pods ?? []).reduce((acc, p) => acc + p.memBytes, 0);
  const podCount = (metrics.data?.pods ?? []).length;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">
          Traffic & runtime
        </h3>
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
        {/* CPU + memory: live from metrics-server. */}
        <ResourceCard
          title="CPU"
          icon={Cpu}
          primary={podCount > 0 ? formatCPU(totalCpu) : "—"}
          subtitle={
            podCount > 0
              ? `across ${podCount} pod${podCount === 1 ? "" : "s"}`
              : "no pods running"
          }
          rows={(metrics.data?.pods ?? []).map((p) => ({
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
          rows={(metrics.data?.pods ?? []).map((p) => ({
            label: p.pod,
            value: formatBytes(p.memBytes),
          }))}
        />

        {/* Traffic metrics: not wired yet. Honest copy + a 'soon' tag
            instead of pretending the data is missing because of
            inactivity. Lands when we plug in a prometheus or
            traefik-metrics source. */}
        <PendingCard
          icon={Activity}
          title="Requests"
          note="Wire a prometheus or traefik-metrics source to populate."
        />
        <PendingCard
          icon={AlertTriangle}
          title="Error rate"
          note="Lands alongside Requests."
        />
        <PendingCard
          icon={Timer}
          title="Response time"
          note="Lands alongside Requests."
        />
      </div>

      {!envName && (
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          No production environment yet — push to the connected branch or hit Redeploy.
        </p>
      )}
      {metrics.isError && (
        <p className="font-mono text-[10px] text-amber-400">
          metrics-server returned an error: {metrics.error?.message}
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
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          live
        </span>
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

function PendingCard({
  icon: Icon,
  title,
  note,
}: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  note: string;
}) {
  return (
    <div className="rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <header className="flex items-center justify-between">
        <h4 className="flex items-center gap-1.5 text-sm font-medium text-[var(--text-secondary)]">
          <Icon className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          {title}
        </h4>
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          soon
        </span>
      </header>
      <p className="mt-3 text-[11px] text-[var(--text-tertiary)]">{note}</p>
    </div>
  );
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
