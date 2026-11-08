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

import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsNotice } from "@/components/settings/SettingsSection";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import { Segmented } from "@/components/ui/segmented";
import { api } from "@/lib/api-client";
import { describeError } from "@/lib/errors";
import { useProjects } from "@/features/projects";
import { Cpu, MemoryStick, Server, Folder, ChevronDown, ChevronRight, Database, Archive } from "lucide-react";
import Link from "next/link";

// Rows are keyed by the pods' project label, which also covers the
// synthetic instance-addon project and history from projects deleted
// inside the window. Servers before `kind` existed report everything as
// a project, so the page classifies against the projects list itself.
type UsageKind = "project" | "platform" | "deleted";
const INSTANCE_ADDON_PROJECT = "kuso-instance";

interface ProjectUsageRow {
  project: string;
  cpuMilliHours: number;
  memGBHours: number;
  cost: number;
  sharePct: number;
  kind?: UsageKind;
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
  { label: "7 days", value: "7" },
  { label: "30 days", value: "30" },
  { label: "90 days", value: "90" },
] as const;

const HOURS_IN_30_DAYS = 30 * 24;

function rowKind(r: ProjectUsageRow, live: ReadonlySet<string> | null): UsageKind {
  if (r.kind) return r.kind;
  if (r.project === INSTANCE_ADDON_PROJECT) return "platform";
  return live && !live.has(r.project) ? "deleted" : "project";
}

// Whole numbers above 10 — a projection has no business showing a
// decimal place on "1597.1".
function qty(n: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: Math.abs(n) < 10 ? 1 : 0 }).format(n);
}

function money(n: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 2 }).format(n);
  } catch {
    return `${currency} ${n.toFixed(2)}`;
  }
}

function rate(n: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumSignificantDigits: 3 }).format(n);
  } catch {
    return `${currency} ${n}`;
  }
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

export default function UsagePage() {
  const [windowDays, setWindowDays] = useState(30);
  const byProject = useQuery<ProjectUsageResponse>({
    queryKey: ["usage", "projects", windowDays],
    queryFn: () => api(`/api/usage/projects?days=${windowDays}`),
  });

  return (
    <SettingsPage
      title="Usage & cost"
      description={`Where CPU and memory go, by project. Figures look ahead 30 days at the pace of the last ${windowDays} days; cost uses the rates set in Cluster config.`}
      actions={
        <Segmented
          label="Based on the last"
          size="sm"
          value={String(windowDays)}
          onChange={(v) => setWindowDays(Number(v))}
          options={WINDOWS}
        />
      }
    >
      {byProject.isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-72 w-full" />
        </div>
      ) : byProject.isError ? (
        <SettingsNotice tone="error" title="Couldn't load usage">
          {describeError(byProject.error, "Try again in a moment.")}
        </SettingsNotice>
      ) : !byProject.data ? null : (
        <UsageBody data={byProject.data} windowDays={windowDays} />
      )}
    </SettingsPage>
  );
}

function UsageBody({ data, windowDays }: { data: ProjectUsageResponse; windowDays: number }) {
  const ratesUnset = data.rates.cpuPerHour === 0 && data.rates.memGBPerHour === 0;
  const projectList = useProjects();
  const live = projectList.data ? new Set(projectList.data.map((p) => p.metadata.name)) : null;
  const projects = data.projects.filter((r) => rowKind(r, live) === "project");
  const platform = data.projects.filter((r) => rowKind(r, live) === "platform");
  const deleted = data.projects.filter((r) => rowKind(r, live) === "deleted");
  const cpuHours = data.clusterTotal.cpuMilliHours / 1000;
  const memGBHours = data.clusterTotal.memGBHours;
  const extras = [
    platform.length > 0 ? "shared platform services" : "",
    deleted.length > 0 ? plural(deleted.length, "deleted project") : "",
  ].filter(Boolean);
  return (
    <div className="space-y-6">
      {ratesUnset && (
        <SettingsNotice tone="info" title="No cost rates set">
          <div>
            Usage is tracked, but there are no prices to turn it into cost. Set{" "}
            <code className="font-mono">spec.cost.cpuPerHour</code> and{" "}
            <code className="font-mono">spec.cost.memGBPerHour</code> in{" "}
            <Link href="/settings/config" className="underline">
              Cluster config
            </Link>{" "}
            to see what each project costs.
          </div>
        </SettingsNotice>
      )}

      {/* Cluster headline cards. */}
      <section className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <Card
          label="Cost, next 30 days"
          big={ratesUnset ? "—" : money(data.clusterTotal.costTotal, data.rates.currency)}
          hint={
            ratesUnset
              ? "Set rates in Cluster config to see cost"
              : `At ${rate(data.rates.cpuPerHour, data.rates.currency)} per CPU-hour and ${rate(data.rates.memGBPerHour, data.rates.currency)} per GB-hour of memory`
          }
        />
        <Card
          label="CPU, next 30 days"
          big={`≈${qty(cpuHours)} CPU-hours`}
          hint={`About ${qty(cpuHours / HOURS_IN_30_DAYS)} CPU cores busy on average, at the current rate`}
          icon={<Cpu className="h-4 w-4" />}
        />
        <Card
          label="Memory, next 30 days"
          big={`≈${qty(memGBHours)} GB-hours`}
          hint={`About ${qty(memGBHours / HOURS_IN_30_DAYS)} GB in use on average, at the current rate`}
          icon={<MemoryStick className="h-4 w-4" />}
        />
      </section>

      {/* Per-project table — the headline section. */}
      <section>
        <header className="mb-3 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <h2 className="text-base font-semibold tracking-tight">By project</h2>
          <span className="text-xs text-[var(--text-tertiary)]">
            {plural(projects.length, "project")}
            {extras.length > 0 && `, plus ${extras.join(" and ")}`} · share of all CPU used
          </span>
        </header>
        {data.projects.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
            No usage recorded in this window yet. kuso measures running projects every 5 minutes, so a new
            install shows numbers a few minutes after its first deploy.
          </p>
        ) : (
          <div className="space-y-4">
            <ProjectTable rows={projects} daily={data.daily} rates={data.rates} ratesUnset={ratesUnset} />
            {(platform.length > 0 || deleted.length > 0) && (
              <div className="space-y-1.5">
                {platform.map((r) => (
                  <AggregateRow
                    key={r.project}
                    icon={<Database className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />}
                    title="Shared platform services"
                    detail="The cluster database and other addons shared by every project"
                    rows={[r]}
                    rates={data.rates}
                    ratesUnset={ratesUnset}
                  />
                ))}
                {deleted.length > 0 && (
                  <AggregateRow
                    icon={<Archive className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />}
                    title={plural(deleted.length, "deleted project")}
                    detail={`Used resources earlier in this window: ${deleted.map((r) => r.project).join(", ")}`}
                    rows={deleted}
                    rates={data.rates}
                    ratesUnset={ratesUnset}
                  />
                )}
              </div>
            )}
          </div>
        )}
      </section>

      {/* Secondary: per-node breakdown, collapsed by default. */}
      <NodeBreakdown windowDays={windowDays} />
    </div>
  );
}

function UsageFigures({
  cpuMilliHours,
  memGBHours,
  cost,
  rates,
  ratesUnset,
}: {
  cpuMilliHours: number;
  memGBHours: number;
  cost: number;
  rates: UsageRates;
  ratesUnset: boolean;
}) {
  return (
    <div className="flex w-full shrink-0 items-center justify-end gap-4 text-xs tabular-nums text-[var(--text-secondary)] sm:w-[300px]">
      <span title="CPU-hours over the next 30 days at the current rate">{qty(cpuMilliHours / 1000)} CPU-h</span>
      <span title="GB-hours of memory over the next 30 days at the current rate">{qty(memGBHours)} GB-h</span>
      {!ratesUnset && (
        <span className="w-[72px] text-right font-semibold text-[var(--text-primary)]">{money(cost, rates.currency)}</span>
      )}
    </div>
  );
}

function AggregateRow({
  icon,
  title,
  detail,
  rows,
  rates,
  ratesUnset,
}: {
  icon: React.ReactNode;
  title: string;
  detail: string;
  rows: ProjectUsageRow[];
  rates: UsageRates;
  ratesUnset: boolean;
}) {
  const sum = (f: (r: ProjectUsageRow) => number) => rows.reduce((a, r) => a + f(r), 0);
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-dashed border-[var(--border-subtle)] px-4 py-3 sm:flex-nowrap">
      <span className="w-3.5 shrink-0" aria-hidden />
      {icon}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium text-[var(--text-primary)]">{title}</span>
        <span className="block text-xs text-[var(--text-tertiary)]">{detail}</span>
      </span>
      <ShareBar pct={sum((r) => r.sharePct)} />
      <UsageFigures
        cpuMilliHours={sum((r) => r.cpuMilliHours)}
        memGBHours={sum((r) => r.memGBHours)}
        cost={sum((r) => r.cost)}
        rates={rates}
        ratesUnset={ratesUnset}
      />
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
              aria-expanded={isOpen}
              onClick={() => setExpanded(isOpen ? null : r.project)}
              className="flex w-full flex-wrap items-center gap-3 px-4 py-3 text-left hover:bg-[var(--bg-tertiary)]/40 sm:flex-nowrap"
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
              <UsageFigures
                cpuMilliHours={r.cpuMilliHours}
                memGBHours={r.memGBHours}
                cost={r.cost}
                rates={rates}
                ratesUnset={ratesUnset}
              />
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
// cluster CPU. Width is clamped to a sane range so a single
// project that's the only one in the cluster (sharePct=100) doesn't
// blow out the row layout.
function ShareBar({ pct }: { pct: number }) {
  const w = Math.min(100, Math.max(0, pct));
  return (
    <div className="flex w-[160px] shrink-0 items-center gap-2">
      <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--bg-tertiary)]">
        <div
          className="h-full bg-[var(--success)]/60"
          style={{ width: `${w}%` }}
        />
      </div>
      <span className="w-9 shrink-0 text-right text-xs tabular-nums text-[var(--text-tertiary)]">
        {pct > 0 && pct < 1 ? "<1" : pct.toFixed(0)}%
      </span>
    </div>
  );
}

// DailySpark renders the per-day CPU-hours curve as ASCII-tall sparkline
// bars. Cheap to render, no chart lib dependency. Tooltips on each
// bar show day + value so hovering tells you the spike date.
function DailySpark({ days }: { days: ProjectCostDay[] }) {
  const max = Math.max(1, ...days.map((d) => d.cpuMilliHours));
  return (
    <div>
      <div className="mb-1 text-xs text-[var(--text-tertiary)]">
        CPU-hours used per day · {plural(days.length, "day")} with data
      </div>
      <div className="flex h-12 items-end gap-1">
        {days.map((d) => {
          const h = Math.max(2, (d.cpuMilliHours / max) * 100);
          const date = new Date(d.day);
          return (
            <div
              key={d.day}
              className="flex-1 rounded-sm bg-[var(--success)]/40"
              style={{ height: `${h}%`, minWidth: 4 }}
              title={`${date.toLocaleDateString(undefined, { month: "short", day: "numeric" })}: ${qty(d.cpuMilliHours / 1000)} CPU-hours`}
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
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 rounded-sm text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
      >
        {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        <h2 className="text-base font-semibold tracking-tight">By node</h2>
        <span className="text-xs">Which servers did the work, over the last {windowDays} days</span>
      </button>
      {open && (
        <div className="mt-3">
          {q.isPending ? (
            <Skeleton className="h-16 w-full" />
          ) : q.isError ? (
            <SettingsNotice tone="error" title="Couldn't load the node breakdown">
              {describeError(q.error, "Try again in a moment.")}
            </SettingsNotice>
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
                    className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3"
                  >
                    <div className="flex items-center gap-3">
                      <Server className="h-4 w-4 text-[var(--text-tertiary)]" />
                      <span className="font-mono text-sm font-medium">{t.node}</span>
                    </div>
                    <div className="flex items-center gap-4 text-xs tabular-nums text-[var(--text-secondary)]">
                      <span title={`CPU-hours used in the last ${windowDays} days`}>{qty(t.cpuMilliHours / 1000)} CPU-h</span>
                      <span title={`GB-hours of memory used in the last ${windowDays} days`}>{qty(t.memGBHours)} GB-h</span>
                      {!ratesUnset && (
                        <span className="font-semibold text-[var(--text-primary)]">{money(cost, q.data.rates.currency)}</span>
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
      <div className="flex items-center gap-2 text-xs font-medium text-[var(--text-secondary)]">
        {icon}
        <span>{label}</span>
      </div>
      <div className="mt-2 font-heading text-2xl font-semibold tracking-tight">{big}</div>
      {hint && <div className="mt-1 text-xs text-[var(--text-tertiary)]">{hint}</div>}
    </div>
  );
}

