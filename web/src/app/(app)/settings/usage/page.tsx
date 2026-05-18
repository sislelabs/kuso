"use client";

// /settings/usage — per-project cost rollup.
//
// v0.13.6 rewrite. Previous version was per-node-only which was
// useless on a single-node cluster (one row that just repeats the
// cluster total). The new page answers the actual operator question:
// "which project is eating my box?"
//
// Data: /api/usage/projects rolls up the per-project sampler
// (projectmetrics.Sampler, 5min cadence × kuso.sislelabs.com/project
// label) into daily totals + a 30-day projection at the operator-
// configured rates. Per-node breakdown is still available below for
// folks who care about node attribution.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api-client";
import { Cpu, MemoryStick, Server, AlertCircle, Folder, ChevronDown, ChevronRight } from "lucide-react";
import Link from "next/link";

interface ProjectUsageRow {
  project: string;
  cpuMilliHours: number;
  memGBHours: number;
  cost: number;
  sharePct: number;
}
interface ProjectCostDay {
  project: string;
  day: string;
  cpuMilliHours: number;
  memGBHours: number;
  sampleCount: number;
}
interface UsageRates {
  cpuPerHour: number;
  memGBPerHour: number;
  currency: string;
}
interface UsageProjection {
  cpuMilliHours: number;
  memGBHours: number;
  costTotal: number;
}
interface ProjectUsageResponse {
  days: number;
  daily: ProjectCostDay[];
  projects: ProjectUsageRow[];
  clusterTotal: UsageProjection;
  rates: UsageRates;
}

// Per-node response (kept for the secondary "By node" section)
interface CostTotal {
  node: string;
  cpuMilliHours: number;
  memGBHours: number;
  days: number;
}
interface NodeUsageResponse {
  days: number;
  totals: CostTotal[];
  rates: UsageRates;
  projected: UsageProjection;
}

const WINDOWS = [
  { label: "7 days", days: 7 },
  { label: "30 days", days: 30 },
  { label: "90 days", days: 90 },
];

export default function UsagePage() {
  const [windowDays, setWindowDays] = useState(30);
  const byProject = useQuery<ProjectUsageResponse>({
    queryKey: ["usage", "projects", windowDays],
    queryFn: () => api(`/api/usage/projects?days=${windowDays}`),
  });

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <header className="mb-6">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Cluster usage</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Per-project CPU + memory attribution over the last {windowDays} days.
          Cost figures use the rates configured on the Kuso CR — see the hint
          below if they&apos;re unset.
        </p>
      </header>

      <div className="mb-6 flex items-center gap-2">
        {WINDOWS.map((w) => (
          <button
            key={w.days}
            type="button"
            onClick={() => setWindowDays(w.days)}
            className={`rounded-md border px-3 py-1.5 font-mono text-[11px] tracking-widest uppercase ${
              w.days === windowDays
                ? "border-[var(--border-strong)] bg-[var(--bg-secondary)] text-[var(--text-primary)]"
                : "border-[var(--border-subtle)] bg-transparent text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)]/40"
            }`}
          >
            {w.label}
          </button>
        ))}
      </div>

      {byProject.isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-72 w-full" />
        </div>
      ) : byProject.isError ? (
        <p className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-300">
          Couldn&apos;t load usage:{" "}
          {byProject.error instanceof Error ? byProject.error.message : "unknown error"}
        </p>
      ) : !byProject.data ? null : (
        <UsageBody data={byProject.data} windowDays={windowDays} />
      )}
    </div>
  );
}

function UsageBody({ data, windowDays }: { data: ProjectUsageResponse; windowDays: number }) {
  const ratesUnset = data.rates.cpuPerHour === 0 && data.rates.memGBPerHour === 0;
  return (
    <div className="space-y-6">
      {ratesUnset && (
        <div className="flex items-start gap-3 rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-200">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <div className="font-semibold">Cost rates not configured</div>
            <div className="mt-1 text-[12px] text-amber-200/80">
              Usage curves and share-of-cluster render, but no dollar figures.
              Set <code className="font-mono">spec.cost.cpuPerHour</code> and{" "}
              <code className="font-mono">spec.cost.memGBPerHour</code> on the
              Kuso CR via{" "}
              <Link href="/settings/config" className="underline">
                Cluster config
              </Link>{" "}
              to enable projection.
            </div>
          </div>
        </div>
      )}

      {/* Cluster headline cards. */}
      <section className="grid gap-3 sm:grid-cols-3">
        <Card
          label={`projected ${data.days === 30 ? "this month" : "next 30 days"}`}
          big={
            ratesUnset
              ? "—"
              : `${data.rates.currency} ${data.clusterTotal.costTotal.toFixed(2)}`
          }
          hint={
            ratesUnset
              ? "configure rates to see cost"
              : `${data.projects.length} project${data.projects.length === 1 ? "" : "s"} · at $${data.rates.cpuPerHour.toFixed(4)}/cpu·hr + $${data.rates.memGBPerHour.toFixed(4)}/GB·hr`
          }
        />
        <Card
          label="CPU consumed"
          big={`${(data.clusterTotal.cpuMilliHours / 1000).toFixed(1)} cpu·hr`}
          hint={`projected over 30 days from a ${windowDays}-day window`}
          icon={<Cpu className="h-4 w-4" />}
        />
        <Card
          label="Memory consumed"
          big={`${data.clusterTotal.memGBHours.toFixed(1)} GB·hr`}
          hint={`avg ${(data.clusterTotal.memGBHours / (30 * 24)).toFixed(2)} GB resident`}
          icon={<MemoryStick className="h-4 w-4" />}
        />
      </section>

      {/* Per-project table — the headline section. */}
      <section>
        <header className="mb-3 flex items-baseline justify-between">
          <h2 className="font-heading text-sm font-semibold tracking-tight">By project</h2>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            window: {data.days} days · {data.projects.length} project
            {data.projects.length === 1 ? "" : "s"}
          </span>
        </header>
        {data.projects.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
            No project samples in the selected window. The per-project sampler
            runs every 5 minutes against pods labelled{" "}
            <code className="font-mono">kuso.sislelabs.com/project</code> —
            a fresh install won&apos;t have data until then, and a cluster
            with no running projects will stay empty.
          </p>
        ) : (
          <ProjectTable rows={data.projects} daily={data.daily} rates={data.rates} ratesUnset={ratesUnset} />
        )}
      </section>

      {/* Secondary: per-node breakdown, collapsed by default. */}
      <NodeBreakdown windowDays={windowDays} />
    </div>
  );
}

function ProjectTable({
  rows,
  daily,
  rates,
  ratesUnset,
}: {
  rows: ProjectUsageRow[];
  daily: ProjectCostDay[];
  rates: UsageRates;
  ratesUnset: boolean;
}) {
  const [expanded, setExpanded] = useState<string | null>(null);
  return (
    <ul className="space-y-1.5">
      {rows.map((r) => {
        const isOpen = expanded === r.project;
        const projectDaily = daily.filter((d) => d.project === r.project);
        return (
          <li
            key={r.project}
            className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
          >
            <button
              type="button"
              onClick={() => setExpanded(isOpen ? null : r.project)}
              className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-[var(--bg-tertiary)]/40"
            >
              {isOpen ? (
                <ChevronDown className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
              ) : (
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
              )}
              <Folder className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
              <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">
                {r.project}
              </span>
              <ShareBar pct={r.sharePct} />
              <div className="flex w-[280px] shrink-0 items-center justify-end gap-4 font-mono text-[11px] text-[var(--text-secondary)]">
                <span title="CPU consumed (projected)">
                  {(r.cpuMilliHours / 1000).toFixed(1)} cpu·hr
                </span>
                <span title="memory consumed (projected)">
                  {r.memGBHours.toFixed(1)} GB·hr
                </span>
                {!ratesUnset && (
                  <span className="w-[70px] text-right font-semibold text-[var(--text-primary)]">
                    {rates.currency} {r.cost.toFixed(2)}
                  </span>
                )}
              </div>
            </button>
            {isOpen && projectDaily.length > 0 && (
              <div className="border-t border-[var(--border-subtle)] bg-[var(--bg-primary)] px-4 py-3">
                <DailySpark days={projectDaily} />
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

// ShareBar renders a horizontal % bar for the project's share of
// cluster CPU·hr. Width is clamped to a sane range so a single
// project that's the only one in the cluster (sharePct=100) doesn't
// blow out the row layout.
function ShareBar({ pct }: { pct: number }) {
  const w = Math.min(100, Math.max(0, pct));
  return (
    <div className="flex w-[160px] shrink-0 items-center gap-2">
      <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--bg-tertiary)]">
        <div
          className="h-full bg-emerald-500/60"
          style={{ width: `${w}%` }}
        />
      </div>
      <span className="w-9 shrink-0 text-right font-mono text-[10px] text-[var(--text-tertiary)]">
        {pct.toFixed(0)}%
      </span>
    </div>
  );
}

// DailySpark renders the per-day cpu·hr curve as ASCII-tall sparkline
// bars. Cheap to render, no chart lib dependency. Tooltips on each
// bar show day + value so hovering tells you the spike date.
function DailySpark({ days }: { days: ProjectCostDay[] }) {
  const max = Math.max(1, ...days.map((d) => d.cpuMilliHours));
  return (
    <div>
      <div className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        cpu·hr per day · last {days.length} sample{days.length === 1 ? "" : "s"}
      </div>
      <div className="flex h-12 items-end gap-1">
        {days.map((d) => {
          const h = Math.max(2, (d.cpuMilliHours / max) * 100);
          const date = new Date(d.day);
          return (
            <div
              key={d.day}
              className="flex-1 rounded-sm bg-emerald-500/40"
              style={{ height: `${h}%`, minWidth: 4 }}
              title={`${date.toISOString().slice(0, 10)} · ${(d.cpuMilliHours / 1000).toFixed(2)} cpu·hr`}
            />
          );
        })}
      </div>
    </div>
  );
}

function NodeBreakdown({ windowDays }: { windowDays: number }) {
  const [open, setOpen] = useState(false);
  const q = useQuery<NodeUsageResponse>({
    queryKey: ["usage", "nodes", windowDays],
    queryFn: () => api(`/api/usage?days=${windowDays}`),
    enabled: open,
  });
  return (
    <section>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <h2 className="font-heading text-sm font-semibold tracking-tight">
          By node
        </h2>
        <span className="font-mono text-[10px]">
          {open ? "(hide)" : "(show)"} — same data, node attribution
        </span>
      </button>
      {open && (
        <div className="mt-3">
          {q.isPending ? (
            <Skeleton className="h-16 w-full" />
          ) : !q.data || q.data.totals.length === 0 ? (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
              No samples in the selected window.
            </p>
          ) : (
            <ul className="space-y-2">
              {q.data.totals.map((t) => {
                const ratesUnset = q.data.rates.cpuPerHour === 0 && q.data.rates.memGBPerHour === 0;
                const cost =
                  (t.cpuMilliHours / 1000) * q.data.rates.cpuPerHour +
                  t.memGBHours * q.data.rates.memGBPerHour;
                return (
                  <li
                    key={t.node}
                    className="flex items-center justify-between rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3"
                  >
                    <div className="flex items-center gap-3">
                      <Server className="h-4 w-4 text-[var(--text-tertiary)]" />
                      <span className="font-mono text-sm font-medium">{t.node}</span>
                    </div>
                    <div className="flex items-center gap-4 font-mono text-[11px] text-[var(--text-secondary)]">
                      <span>{(t.cpuMilliHours / 1000).toFixed(1)} cpu·hr</span>
                      <span>{t.memGBHours.toFixed(1)} GB·hr</span>
                      {!ratesUnset && (
                        <span className="font-semibold text-[var(--text-primary)]">
                          {q.data.rates.currency} {cost.toFixed(2)}
                        </span>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}

function Card({
  label,
  big,
  hint,
  icon,
}: {
  label: string;
  big: string;
  hint?: string;
  icon?: React.ReactNode;
}) {
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <div className="flex items-center gap-2 text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {icon}
        <span>{label}</span>
      </div>
      <div className="mt-2 font-heading text-2xl font-semibold tracking-tight">{big}</div>
      {hint && <div className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">{hint}</div>}
    </div>
  );
}

