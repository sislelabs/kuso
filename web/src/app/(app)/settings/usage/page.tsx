"use client";

// /settings/usage — cluster cost rollup.
//
// Aggregates NodeMetric samples into per-node total CPU·hours and
// GB·hours over the configured window (default 30 days). When the
// operator has set spec.cost.{cpuPerHour, memGBPerHour} on the Kuso
// CR, the page renders a dollar projection alongside the raw usage;
// otherwise it shows only the raw curves with a "configure rates"
// hint pointing at /settings/config.
//
// Per-project breakdown is a follow-up — the underlying NodeMetric
// stream is per-node and per-project attribution needs either a new
// sampler dimension or a pod-count weighting estimate. v1 ships
// per-node, which matches how operators are billed by their cloud
// provider anyway.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api-client";
import { Cpu, MemoryStick, Server, AlertCircle } from "lucide-react";
import Link from "next/link";

interface CostRollupDay {
  node: string;
  day: string;
  cpuMilliHours: number;
  memGBHours: number;
  sampleCount: number;
}
interface CostTotal {
  node: string;
  cpuMilliHours: number;
  memGBHours: number;
  days: number;
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
interface UsageResponse {
  days: number;
  daily: CostRollupDay[];
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
  const q = useQuery<UsageResponse>({
    queryKey: ["usage", windowDays],
    queryFn: () => api(`/api/usage?days=${windowDays}`),
  });

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <header className="mb-6">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Cluster usage</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Per-node CPU + memory consumption over the last {windowDays} days, aggregated from the
          background sampler. Cost figures use the rates configured on the Kuso CR — see the hint
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

      {q.isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-72 w-full" />
        </div>
      ) : q.isError ? (
        <p className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-300">
          Couldn&apos;t load usage: {q.error instanceof Error ? q.error.message : "unknown error"}
        </p>
      ) : !q.data ? null : (
        <UsageBody data={q.data} />
      )}
    </div>
  );
}

function UsageBody({ data }: { data: UsageResponse }) {
  const ratesUnset = data.rates.cpuPerHour === 0 && data.rates.memGBPerHour === 0;
  return (
    <div className="space-y-6">
      {ratesUnset && (
        <div className="flex items-start gap-3 rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-200">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <div className="font-semibold">Cost rates not configured</div>
            <div className="mt-1 text-[12px] text-amber-200/80">
              Usage curves render, but no dollar figures. Set{" "}
              <code className="font-mono">spec.cost.cpuPerHour</code> and{" "}
              <code className="font-mono">spec.cost.memGBPerHour</code> on the Kuso CR via{" "}
              <Link href="/settings/config" className="underline">
                Cluster config
              </Link>{" "}
              to enable projection.
            </div>
          </div>
        </div>
      )}

      {/* Headline projection card. */}
      <section className="grid gap-3 sm:grid-cols-3">
        <Card
          label={`projected ${data.days === 30 ? "this month" : "next 30 days"}`}
          big={
            ratesUnset
              ? "—"
              : `${data.rates.currency} ${data.projected.costTotal.toFixed(2)}`
          }
          hint={`at $${data.rates.cpuPerHour.toFixed(4)}/cpu·hr + $${data.rates.memGBPerHour.toFixed(4)}/GB·hr`}
        />
        <Card
          label="CPU consumed"
          big={`${(data.projected.cpuMilliHours / 1000).toFixed(1)} cpu·hr`}
          hint={`projected over 30 days from a ${data.days}-day window`}
          icon={<Cpu className="h-4 w-4" />}
        />
        <Card
          label="Memory consumed"
          big={`${data.projected.memGBHours.toFixed(1)} GB·hr`}
          hint={`avg ${(data.projected.memGBHours / (30 * 24)).toFixed(2)} GB resident`}
          icon={<MemoryStick className="h-4 w-4" />}
        />
      </section>

      {/* Per-node totals. */}
      <section>
        <header className="mb-3 flex items-baseline justify-between">
          <h2 className="font-heading text-sm font-semibold tracking-tight">Per-node totals</h2>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            window: {data.days} days · {data.totals.length} node{data.totals.length === 1 ? "" : "s"}
          </span>
        </header>
        {data.totals.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
            No samples in the selected window. The sampler runs every 5 minutes — a fresh install
            won&apos;t have data until then.
          </p>
        ) : (
          <ul className="space-y-2">
            {data.totals.map((t) => {
              const cost =
                (t.cpuMilliHours / 1000) * data.rates.cpuPerHour +
                t.memGBHours * data.rates.memGBPerHour;
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
                        {data.rates.currency} {cost.toFixed(2)}
                      </span>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
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
