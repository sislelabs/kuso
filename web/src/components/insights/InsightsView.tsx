"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Check, Link2, Search, X } from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/shared/PageHeader";
import { TimeRangePicker } from "@/components/shared/TimeRangePicker";
import { Segmented } from "@/components/ui/segmented";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useInsights, type Insights, type InsightsPoint, type InsightsRange, type InsightsService } from "@/features/insights";
import { cn } from "@/lib/utils";
import {
  MAX_SELECTED,
  METRICS,
  PRESETS,
  RANGES,
  SERIES_COLORS,
  defaultSelection,
  deployEffect,
  groupDeploys,
  metricDef,
  seriesStats,
  serviceId,
  serviceLabel,
  type DeployEffect,
  type MetricDef,
  type MetricKey,
} from "./insightsModel";
import { InsightsChart, type ChartDeploy, type ChartSeries } from "./InsightsChart";

const panel = "rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-secondary)]";
const MAX_DEPLOY_ROWS = 10;

function isMetric(v: string | null): v is MetricKey {
  return METRICS.some((m) => m.key === v);
}

function isRange(v: string | null): v is InsightsRange {
  return RANGES.some((r) => r === v);
}

// URL state: ?m=<metric>&r=<range>&s=<project/service,...>. Copy link
// shares exactly what's on screen.
function useInsightsParams() {
  const search = useSearchParams();
  const router = useRouter();
  const pathname = usePathname() ?? "/insights";
  const m = search.get("m");
  const r = search.get("r");
  const s = search.get("s");
  const metric: MetricKey = isMetric(m) ? m : "requests";
  const range: InsightsRange = isRange(r) ? r : "24h";
  const selected = s === null ? null : s.split(",").filter(Boolean);
  const set = (next: { metric?: MetricKey; range?: InsightsRange; selected?: string[] }) => {
    const q = new URLSearchParams(search.toString());
    if (next.metric) q.set("m", next.metric);
    if (next.range) q.set("r", next.range);
    if (next.selected) q.set("s", next.selected.join(","));
    router.replace(`${pathname}?${q.toString()}`, { scroll: false });
  };
  return { metric, range, selected, set };
}

function Delta({ effect, def }: { effect: DeployEffect | null; def: MetricDef }) {
  if (!effect) return <span className="text-[var(--text-tertiary)]">—</span>;
  if (Math.abs(effect.pct) < 5) return <span className="text-xs text-[var(--text-tertiary)]">≈ same</span>;
  const up = effect.pct > 0;
  const tone = !def.higherIsWorse
    ? "bg-[var(--accent-subtle)] text-[var(--accent)]"
    : up
      ? "bg-[var(--danger-subtle)] text-[var(--danger)]"
      : "bg-[var(--success-subtle)] text-[var(--success)]";
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-full px-2 py-px text-xs", tone)}>
      {up ? "▲" : "▼"} {Math.abs(effect.pct).toFixed(0)}%
    </span>
  );
}

function Spark({ points }: { points: InsightsPoint[] }) {
  if (points.length < 2) return <span className="h-4 w-14" />;
  const W = 56;
  const H = 16;
  const t0 = points[0][0];
  const span = points[points.length - 1][0] - t0 || 1;
  const max = Math.max(...points.map((p) => p[1])) || 1;
  const d = points.map(([t, v], i) => `${i ? "L" : "M"}${(((t - t0) / span) * W).toFixed(1)},${(H - 2 - (v / max) * (H - 4)).toFixed(1)}`).join("");
  return (
    <svg aria-hidden width={W} height={H} className="shrink-0">
      <path d={d} fill="none" className="stroke-[var(--text-tertiary)]" strokeWidth={1.2} />
    </svg>
  );
}

export function InsightsView() {
  const { metric, range, selected: urlSelected, set } = useInsightsParams();
  const query = useInsights(range);
  const data = query.data;
  const def = metricDef(metric);

  return (
    <div className="mx-auto max-w-[96rem] px-4 py-6 sm:px-6 lg:px-10 lg:py-9">
      <PageHeader
        title="Insights"
        description={<Lede data={data} />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <TimeRangePicker presets={RANGES} value={range} onChange={(r) => set({ range: r })} />
            <CopyLink />
          </div>
        }
      />
      {query.isPending && <InsightsSkeleton />}
      {query.isError && !data && <QueryErrorState error={query.error} onRetry={() => query.refetch()} what="insights" />}
      {data && (
        <Loaded
          data={data}
          def={def}
          urlSelected={urlSelected}
          onMetric={(m) => set({ metric: m })}
          onSelect={(s, m) => set({ selected: s, metric: m ?? undefined })}
          dim={query.isPlaceholderData}
        />
      )}
    </div>
  );
}

function Lede({ data }: { data: Insights | undefined }) {
  if (!data) return "Compare services across requests, CPU, memory, latency and errors.";
  const pushes = groupDeploys(data.deploys).length;
  return `Compare services across requests, CPU, memory, latency and errors. ${data.services.length} production services, ${pushes} ${pushes === 1 ? "deploy" : "deploys"} in the last ${data.range}.`;
}

function CopyLink() {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(window.location.href);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          toast.error("Couldn't copy the link");
        }
      }}
      className="inline-flex h-9 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] px-3 text-[13px] font-medium text-[var(--text-primary)] outline-none transition-colors hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
    >
      {copied ? <Check className="size-4" aria-hidden /> : <Link2 className="size-4" aria-hidden />}
      {copied ? "Copied" : "Copy link"}
    </button>
  );
}

function Loaded({
  data,
  def,
  urlSelected,
  onMetric,
  onSelect,
  dim,
}: {
  data: Insights;
  def: MetricDef;
  urlSelected: string[] | null;
  onMetric: (m: MetricKey) => void;
  onSelect: (ids: string[], metric?: MetricKey | null) => void;
  dim: boolean;
}) {
  const [showDeploys, setShowDeploys] = useState(true);
  const byId = useMemo(() => new Map(data.services.map((s) => [serviceId(s), s])), [data.services]);
  const selected = useMemo(
    () => (urlSelected ?? defaultSelection(data.services)).filter((id) => byId.has(id)).slice(0, MAX_SELECTED),
    [urlSelected, data.services, byId],
  );
  const color = (id: string) => SERIES_COLORS[selected.indexOf(id)] ?? SERIES_COLORS[0];
  const toggle = (id: string) => {
    if (selected.includes(id)) onSelect(selected.filter((x) => x !== id));
    else if (selected.length >= MAX_SELECTED) toast(`Compare up to ${MAX_SELECTED} services at once`);
    else onSelect([...selected, id]);
  };

  const t0 = Date.parse(data.since) / 1000;
  const t1 = Date.parse(data.until) / 1000;
  const rangeSeconds = t1 - t0;
  const missing = def.source === "traffic" ? !data.requestsAvailable : !data.usageAvailable;
  const sel = selected.map((id) => byId.get(id)).filter((s): s is InsightsService => !!s);

  const deploysBySvc = useMemo(() => {
    const m = new Map<string, number[]>();
    for (const d of data.deploys) {
      const id = serviceId(d);
      const list = m.get(id) ?? [];
      list.push(Date.parse(d.at) / 1000);
      m.set(id, list);
    }
    return m;
  }, [data.deploys]);
  const groups = useMemo(() => groupDeploys(data.deploys), [data.deploys]);

  const series: ChartSeries[] = sel.map((s) => ({ id: serviceId(s), label: serviceLabel(s), color: color(serviceId(s)), points: s[def.key] }));
  const chartDeploys: ChartDeploy[] = showDeploys
    ? groups.flatMap((g) => {
        const hit = g.services.map((svc) => `${g.project}/${svc}`).filter((id) => selected.includes(id));
        if (!hit.length) return [];
        return [{ at: g.at, color: color(hit[0]), title: `${g.project} deployed ${g.services.join(", ")}`, detail: g.commitMessage }];
      })
    : [];

  return (
    <div className="grid items-start gap-5 lg:grid-cols-[17rem_minmax(0,1fr)]">
      <ServiceRail data={data} def={def} selected={selected} color={color} onToggle={toggle} onSelect={onSelect} />

      <div className="min-w-0 space-y-6">
        <section className={cn(panel, "px-4 pb-3 pt-4 sm:px-5")}>
          <div className="mb-3 flex flex-wrap items-center gap-3">
            <Segmented label="Metric" options={METRICS.map((m) => ({ value: m.key, label: m.label }))} value={def.key} onChange={onMetric} />
            <label className="ml-auto flex cursor-pointer items-center gap-2 text-xs text-[var(--text-secondary)]">
              <input type="checkbox" checked={showDeploys} onChange={(e) => setShowDeploys(e.target.checked)} className="accent-[var(--accent)]" />
              Deploys
            </label>
          </div>
          <div className="mb-2 flex min-h-7 flex-wrap gap-1.5">
            {sel.map((s) => (
              <span key={serviceId(s)} className="inline-flex items-center gap-1.5 rounded-full bg-[var(--bg-tertiary)] py-0.5 pl-2.5 pr-1 text-[12.5px] text-[var(--text-primary)]">
                <span className="size-2 rounded-full" style={{ background: color(serviceId(s)) }} />
                {serviceLabel(s)}
                <button
                  type="button"
                  aria-label={`Remove ${serviceLabel(s)}`}
                  onClick={() => toggle(serviceId(s))}
                  className="rounded-full p-0.5 text-[var(--text-tertiary)] outline-none hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                >
                  <X className="size-3" aria-hidden />
                </button>
              </span>
            ))}
          </div>
          <InsightsChart
            series={series}
            deploys={chartDeploys}
            domain={[t0, t1]}
            stepSeconds={data.stepSeconds}
            format={def.format}
            tick={def.tick}
            tickUnit={def.tickUnit}
            ariaLabel={`${def.label} over the last ${data.range} for ${sel.map(serviceLabel).join(", ") || "no services"}`}
            empty={
              missing
                ? def.source === "traffic"
                  ? "Prometheus isn't answering, so there's no traffic data"
                  : "No usage history on this server yet"
                : sel.length === 0
                  ? "Pick a service on the left"
                  : undefined
            }
            dim={dim}
          />
        </section>

        <section>
          <h2 className="mb-2.5 font-heading text-[15px] font-semibold text-[var(--text-primary)]">{def.label} for the selected services</h2>
          <div className={cn(panel, "overflow-x-auto")}>
            <table className="w-full min-w-[36rem] text-right text-[13px]">
              <thead>
                <tr className="border-b border-[var(--border-subtle)] font-mono text-[11px] uppercase tracking-wide text-[var(--text-tertiary)]">
                  <th className="px-4 py-2.5 text-left font-medium">Service</th>
                  <th className="px-4 py-2.5 font-medium">Now</th>
                  <th className="px-4 py-2.5 font-medium">Average</th>
                  <th className="px-4 py-2.5 font-medium">Peak</th>
                  <th className="px-4 py-2.5 font-medium">Deploys</th>
                  <th className="px-4 py-2.5 font-medium">Since last deploy</th>
                </tr>
              </thead>
              <tbody>
                {sel.length === 0 && (
                  <tr>
                    <td colSpan={6} className="px-4 py-6 text-center text-[var(--text-tertiary)]">
                      No services selected
                    </td>
                  </tr>
                )}
                {sel.map((s) => {
                  const id = serviceId(s);
                  const st = seriesStats(s[def.key]);
                  const times = deploysBySvc.get(id) ?? [];
                  const last = times.length ? Math.max(...times) : null;
                  const eff = last === null ? null : deployEffect(s[def.key], last, rangeSeconds, data.stepSeconds);
                  return (
                    <tr key={id} className="border-b border-[var(--border-subtle)] font-mono text-[var(--text-primary)] last:border-0">
                      <td className="px-4 py-2.5 text-left font-sans">
                        <Link
                          href={`/projects/${encodeURIComponent(s.project)}?service=${encodeURIComponent(s.service)}`}
                          className="inline-flex items-center gap-2 rounded-sm outline-none hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                        >
                          <span className="size-2 rounded-full" style={{ background: color(id) }} />
                          {serviceLabel(s)}
                        </Link>
                      </td>
                      <td className="px-4 py-2.5">{st ? def.format(st.now) : "—"}</td>
                      <td className="px-4 py-2.5">{st ? def.format(st.avg) : "—"}</td>
                      <td className="px-4 py-2.5">{st ? def.format(st.peak) : "—"}</td>
                      <td className="px-4 py-2.5">{times.length || "—"}</td>
                      <td className="px-4 py-2.5">
                        <Delta effect={eff} def={def} />
                        {eff && Math.abs(eff.pct) >= 5 && (
                          <div className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">
                            {def.format(eff.before)} → {def.format(eff.after)}
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>

        <section>
          <h2 className="mb-2.5 font-heading text-[15px] font-semibold text-[var(--text-primary)]">Deploys in this range</h2>
          <div className={cn(panel, "overflow-hidden")}>
            {groups.length === 0 && <p className="px-4 py-6 text-center text-[13px] text-[var(--text-tertiary)]">No deploys in the last {data.range}</p>}
            <ul>
              {groups.slice(0, MAX_DEPLOY_ROWS).map((g) => {
                const ids = g.services.map((svc) => `${g.project}/${svc}`).filter((id) => byId.has(id));
                const sumAt = (at: number) => {
                  const effs = ids.map((id) => deployEffect(byId.get(id)![def.key], at, rangeSeconds, data.stepSeconds));
                  if (!effs.length || effs.some((e) => !e)) return null;
                  const before = effs.reduce((n, e) => n + e!.before, 0);
                  const after = effs.reduce((n, e) => n + e!.after, 0);
                  return before ? { before, after, pct: ((after - before) / before) * 100 } : null;
                };
                const eff = sumAt(g.at);
                return (
                  <li key={`${g.project}-${g.commitSha}-${g.at}`} className="border-b border-[var(--border-subtle)] last:border-0">
                    <button
                      type="button"
                      disabled={!ids.length}
                      onClick={() => onSelect(ids.slice(0, MAX_SELECTED))}
                      className="grid w-full grid-cols-[3.5rem_minmax(0,1fr)_auto] items-center gap-3 px-4 py-2.5 text-left text-[13px] outline-none hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] disabled:cursor-default sm:grid-cols-[3.5rem_minmax(0,16rem)_minmax(0,1fr)_auto]"
                    >
                      <span className="font-mono text-xs text-[var(--text-tertiary)]">
                        {new Date(g.at * 1000).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })}
                      </span>
                      <span className="truncate text-[var(--text-primary)]">
                        {g.project}
                        {(g.services.length > 1 || g.services[0] !== g.project) && <span className="text-[var(--text-tertiary)]"> / {g.services.join(", ")}</span>}
                      </span>
                      <span className="hidden truncate text-[var(--text-secondary)] sm:block">{g.commitMessage}</span>
                      <span className="flex items-center justify-end gap-1.5 whitespace-nowrap text-[11.5px] text-[var(--text-tertiary)]">
                        {eff && (
                          <>
                            {def.label.toLowerCase()} <Delta effect={eff} def={def} />
                          </>
                        )}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </div>
        </section>
      </div>
    </div>
  );
}

function ServiceRail({
  data,
  def,
  selected,
  color,
  onToggle,
  onSelect,
}: {
  data: Insights;
  def: MetricDef;
  selected: string[];
  color: (id: string) => string;
  onToggle: (id: string) => void;
  onSelect: (ids: string[], metric?: MetricKey | null) => void;
}) {
  const [q, setQ] = useState("");
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const by = new Map<string, InsightsService[]>();
    for (const s of data.services) {
      if (needle && !serviceLabel(s).toLowerCase().includes(needle)) continue;
      const list = by.get(s.project) ?? [];
      list.push(s);
      by.set(s.project, list);
    }
    return [...by.entries()];
  }, [data.services, q]);

  return (
    <aside className={cn(panel, "order-2 flex max-h-[22rem] flex-col lg:order-none lg:sticky lg:top-16 lg:max-h-[calc(100vh-5rem)]")} aria-label="Services">
      <div className="p-2.5">
        <label className="flex h-8 items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2.5 text-[var(--text-tertiary)] focus-within:ring-2 focus-within:ring-[var(--ring)]">
          <Search className="size-3.5 shrink-0" aria-hidden />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Find a service"
            aria-label="Find a service"
            className="min-w-0 flex-1 bg-transparent text-[13px] text-[var(--text-primary)] outline-none placeholder:text-[var(--text-placeholder)]"
          />
        </label>
      </div>
      <div className="flex flex-wrap gap-1.5 border-b border-[var(--border-subtle)] px-2.5 pb-2.5">
        {PRESETS.map((p) => (
          <button
            key={p.label}
            type="button"
            onClick={() => {
              const ids = p.pick(data.services, data.deploys);
              if (!ids.length) toast(`Nothing matches "${p.label}" in the last ${data.range}`);
              else onSelect(ids, p.metric);
            }}
            className="rounded-full border border-[var(--border-subtle)] px-2.5 py-0.5 text-[11.5px] text-[var(--text-secondary)] outline-none hover:border-[var(--border-default)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
          >
            {p.label}
          </button>
        ))}
      </div>
      <div className="relative min-h-0 flex-1 overflow-y-auto px-1.5 pb-2.5">
        {groups.length === 0 && <p className="px-2 py-4 text-[13px] text-[var(--text-tertiary)]">No services match</p>}
        {groups.map(([project, svcs]) => (
          <div key={project}>
            <div className="flex items-center justify-between px-2 pb-1 pt-2.5 font-mono text-[10.5px] uppercase tracking-wider text-[var(--text-tertiary)]">
              {project}
              {svcs.length > 1 && (
                <button
                  type="button"
                  onClick={() => onSelect(svcs.map(serviceId).slice(0, MAX_SELECTED))}
                  className="rounded-sm font-sans text-[11px] normal-case tracking-normal outline-none hover:text-[var(--accent)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                  aria-label={`Compare all of ${project}`}
                >
                  all
                </button>
              )}
            </div>
            {svcs.map((s) => {
              const id = serviceId(s);
              const on = selected.includes(id);
              return (
                <label
                  key={id}
                  className={cn(
                    "flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] hover:bg-[var(--bg-tertiary)] has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-[var(--ring)]",
                    on ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]" : "text-[var(--text-secondary)]",
                  )}
                >
                  <input type="checkbox" checked={on} onChange={() => onToggle(id)} className="sr-only" />
                  <span
                    aria-hidden
                    className={cn("size-3.5 shrink-0 rounded-[4px] border-[1.5px]", on ? "border-transparent" : "border-[var(--border-default)]")}
                    style={on ? { background: color(id) } : undefined}
                  />
                  <span className="min-w-0 flex-1 truncate">{s.service}</span>
                  <Spark points={s[def.key]} />
                </label>
              );
            })}
          </div>
        ))}
      </div>
    </aside>
  );
}

function InsightsSkeleton() {
  return (
    <div className="grid gap-5 lg:grid-cols-[17rem_minmax(0,1fr)]" aria-busy="true" aria-label="Loading insights">
      <Skeleton className="h-[28rem] rounded-xl" />
      <div className="space-y-6">
        <Skeleton className="h-[30rem] rounded-xl" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
    </div>
  );
}
