"use client";

import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { formatCpu, relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { AttentionItem, FleetCell, FootprintSlice, RecentDeploy } from "./fleetModel";
import { formatBytes } from "./projectCardModel";

const label = "text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--text-tertiary)]";

// One cell per service in the fleet, grouped by project with a gap
// between projects: the whole estate's health in a single glance.
function ServiceStrip({ cells }: { cells: ReadonlyArray<FleetCell> }) {
  return (
    <div className="mt-3 flex w-full flex-wrap items-center gap-[3px]" role="img" aria-label="Status of every service">
      {cells.map((c, i) => {
        const newProject = i > 0 && cells[i - 1].project !== c.project;
        const meta = SERVICE_STATUS[c.status];
        return (
          <Link
            key={`${c.project}/${c.service}`}
            href={`/projects/${c.project}?service=${encodeURIComponent(c.service)}`}
            title={`${c.project} / ${c.service}: ${meta.label}`}
            tabIndex={-1}
            className={cn(
              "h-2.5 w-2.5 rounded-[3px] opacity-85 transition-[opacity,transform] hover:scale-125 hover:opacity-100",
              TONE_CLASSES[meta.tone].dot,
              (c.status === "stopped" || c.status === "sleeping") && "bg-[var(--border-strong)] opacity-60",
              meta.pulse && "motion-safe:animate-pulse",
              newProject && "ml-1.5",
            )}
          />
        );
      })}
    </div>
  );
}

export function FleetPulse({
  cells,
  errors,
  topErrorProject,
  footprint,
  deploys,
}: {
  cells: ReadonlyArray<FleetCell>;
  errors: number | undefined;
  topErrorProject: { name: string; count: number } | undefined;
  footprint: { total: number; cpuMillicores: number; slices: FootprintSlice[]; other: number };
  deploys: ReadonlyArray<RecentDeploy>;
}) {
  const live = cells.filter((c) => c.status === "live").length;
  const resting = cells.filter((c) => c.status === "stopped" || c.status === "sleeping").length;

  return (
    <section
      aria-label="Fleet overview"
      className="mb-6 grid overflow-hidden rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] shadow-node md:grid-cols-2 xl:grid-cols-[1.6fr_1fr_1.3fr_1.3fr]"
    >
      <div className="border-b border-[var(--border-subtle)] p-4 md:border-r xl:border-b-0">
        <p className={label}>Services</p>
        <p className="mt-1.5 flex items-baseline gap-1.5 font-display text-2xl font-semibold tabular-nums text-[var(--text-primary)]">
          {live}
          <span className="text-sm font-normal text-[var(--text-tertiary)]">/ {cells.length} live</span>
          {resting > 0 && <span className="text-xs font-normal text-[var(--text-tertiary)]">· {resting} resting</span>}
        </p>
        <ServiceStrip cells={cells} />
      </div>

      <div className="border-b border-[var(--border-subtle)] p-4 xl:border-b-0 xl:border-r">
        <p className={label}>Errors · 24h</p>
        <p
          className={cn(
            "mt-1.5 font-display text-2xl font-semibold tabular-nums",
            errors ? "text-[var(--danger)]" : "text-[var(--text-primary)]",
          )}
        >
          {errors === undefined ? "—" : errors.toLocaleString()}
        </p>
        <p className="mt-2 truncate text-xs text-[var(--text-tertiary)]">
          {topErrorProject ? (
            <>
              Most from{" "}
              <Link
                href={`/projects/${topErrorProject.name}`}
                className="rounded-sm text-[var(--text-secondary)] underline-offset-2 outline-none hover:text-[var(--text-primary)] hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
              >
                {topErrorProject.name}
              </Link>{" "}
              ({topErrorProject.count.toLocaleString()})
            </>
          ) : errors === 0 ? (
            "No runtime errors logged"
          ) : (
            "Counting…"
          )}
        </p>
      </div>

      <div className="border-b border-[var(--border-subtle)] p-4 md:border-b-0 md:border-r">
        <p className={label}>Footprint</p>
        <p className="mt-1.5 font-display text-2xl font-semibold tabular-nums text-[var(--text-primary)]">
          {formatBytes(footprint.total)}
          <span className="ml-1.5 text-sm font-normal text-[var(--text-tertiary)]">{formatCpu(footprint.cpuMillicores)} CPU</span>
        </p>
        {footprint.slices.length > 0 && (
          <>
            <div className="mt-2.5 flex h-1.5 w-full overflow-hidden rounded-full bg-[var(--bg-tertiary)]" aria-hidden>
              {footprint.slices.map((s, i) => (
                <span
                  key={s.project}
                  title={`${s.project}: ${formatBytes(s.memBytes)}`}
                  className="h-full border-r border-[var(--bg-elevated)] bg-[var(--text-secondary)]"
                  style={{ width: `${s.share * 100}%`, opacity: 1 - i * 0.18 }}
                />
              ))}
            </div>
            <p className="mt-2 truncate text-xs text-[var(--text-tertiary)]">
              {footprint.slices.slice(0, 2).map((s, i) => (
                <span key={s.project}>
                  {i > 0 && " · "}
                  <span className="text-[var(--text-secondary)]">{s.project}</span> {formatBytes(s.memBytes)}
                </span>
              ))}
            </p>
          </>
        )}
      </div>

      <div className="p-4">
        <p className={label}>Recent deploys</p>
        {deploys.length === 0 ? (
          <p className="mt-2 text-xs text-[var(--text-tertiary)]">Nothing deployed yet</p>
        ) : (
          <ol className="mt-2 space-y-1">
            {deploys.map((d) => (
              <li key={`${d.project}/${d.service}/${d.env}`} className="flex min-w-0 items-baseline gap-2 text-xs">
                <Link
                  href={`/projects/${d.project}?service=${encodeURIComponent(d.service)}${d.env !== "production" ? `&env=${encodeURIComponent(d.env)}` : ""}`}
                  className="min-w-0 flex-1 truncate rounded-sm outline-none hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                >
                  <span className="text-[var(--text-primary)]">{d.service}</span>
                  <span className="text-[var(--text-tertiary)]"> · {d.project}</span>
                  {d.env !== "production" && <span className="text-[var(--warning)]"> · {d.env}</span>}
                </Link>
                <span className="shrink-0 tabular-nums text-[var(--text-tertiary)]">{relativeTime(d.at)}</span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </section>
  );
}

const MAX_ATTENTION = 5;

// Only rendered when something is wrong or moving; a healthy fleet shows
// no empty "all good" box.
export function AttentionList({ items }: { items: ReadonlyArray<AttentionItem> }) {
  if (items.length === 0) return null;
  const shown = items.slice(0, MAX_ATTENTION);
  const more = items.length - shown.length;
  return (
    <section aria-label="Needs attention" className="mb-6">
      <p className={cn(label, "mb-2")}>Needs attention</p>
      <ul className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)]">
        {shown.map((a) => {
          const meta = SERVICE_STATUS[a.status];
          return (
            <li key={`${a.project}/${a.service}`}>
              <Link
                href={`/projects/${a.project}?service=${encodeURIComponent(a.service)}`}
                className="group flex min-w-0 items-center gap-3 px-4 py-2.5 text-[13px] outline-none hover:bg-[var(--bg-secondary)] focus-visible:bg-[var(--bg-secondary)]"
              >
                <span
                  aria-hidden
                  className={cn("h-2 w-2 shrink-0 rounded-full", TONE_CLASSES[a.tone].dot, meta.pulse && "motion-safe:animate-pulse")}
                />
                <span className="shrink-0 font-medium text-[var(--text-primary)]">
                  {a.project}
                  <span className="text-[var(--text-tertiary)]"> / </span>
                  {a.service}
                </span>
                <span className={cn("shrink-0", TONE_CLASSES[a.tone].text)}>{meta.label}</span>
                {a.detail && <span className="min-w-0 truncate text-[var(--text-tertiary)]">{a.detail}</span>}
                <ArrowRight
                  aria-hidden
                  className="ml-auto h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)] opacity-0 transition-opacity group-hover:opacity-100"
                />
              </Link>
            </li>
          );
        })}
        {more > 0 && <li className="px-4 py-2 text-xs text-[var(--text-tertiary)]">+{more} more below</li>}
      </ul>
    </section>
  );
}
