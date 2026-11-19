"use client";

import Link from "next/link";
import { ArrowRight, ChartLine } from "lucide-react";
import { formatCpu, relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { ProjectsPulse } from "@/features/projects";
import type { AttentionItem } from "./fleetModel";
import { formatBytes } from "./projectCardModel";

const inlineLink =
  "rounded-sm text-[var(--text-primary)] underline decoration-[var(--border-strong)] underline-offset-[3px] outline-none hover:decoration-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]";

const MAX_PROBLEMS = 4;

type Point = readonly [number, number];

// Sparkline is a filled area for a small series; the path scales to
// whatever width the cell gives it.
function Sparkline({ points, className }: { points: ReadonlyArray<Point>; className?: string }) {
  if (points.length < 2) return null;
  const W = 120;
  const H = 28;
  const t0 = points[0][0];
  const span = points[points.length - 1][0] - t0 || 1;
  const max = Math.max(...points.map((p) => p[1])) || 1;
  const d = points
    .map(([t, v], i) => `${i ? "L" : "M"}${(((t - t0) / span) * W).toFixed(1)},${(H - (v / max) * (H - 3)).toFixed(1)}`)
    .join("");
  return (
    <svg aria-hidden viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className={className}>
      <path d={`${d}L${W},${H}L0,${H}Z`} className="fill-[var(--accent)] opacity-15" />
      <path d={d} className="fill-none stroke-[var(--accent)]" strokeWidth={1.4} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

// DeployTicks marks each deploy on a 24h line.
function DeployTicks({ times, since, className }: { times: ReadonlyArray<number>; since: number; className?: string }) {
  const W = 300;
  const H = 28;
  const span = 24 * 3600 * 1000;
  return (
    <svg aria-hidden viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className={className}>
      <line x1={0} x2={W} y1={H - 6} y2={H - 6} className="stroke-[var(--border-subtle)]" vectorEffect="non-scaling-stroke" />
      {times.map((t, i) => (
        <rect
          key={`${t}-${i}`}
          x={Math.min(W - 3, Math.max(0, ((t - since) / span) * W - 1.5))}
          y={6}
          width={3}
          height={H - 12}
          rx={1.5}
          className="fill-[var(--accent)]"
        />
      ))}
    </svg>
  );
}

const cell = "flex min-w-0 items-center gap-3 px-4 py-3 sm:px-5";
// Cells that open Insights on their metric.
const cellLink =
  "outline-none transition-colors hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)]";
const big = "font-display text-[17px] font-semibold leading-tight text-[var(--text-primary)] [font-stretch:108%] tabular-nums";
const unit = "font-sans text-[13px] font-medium text-[var(--text-tertiary)] [font-stretch:100%]";
const small = "truncate text-xs text-[var(--text-tertiary)]";

function compact(n: number): string {
  if (n >= 10_000) return `${Math.round(n / 1000)}k`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(Math.round(n));
}

export interface FleetStripCapacity {
  label: string;
  memUsed: number;
  memTotal?: number;
  cpuUsedMilli: number;
  cpuTotalMilli?: number;
}

// FleetStrip is the home page's one-row summary: what runs, the last
// day's traffic, errors, capacity in use and deploys, each with a small
// picture where there is a series to draw.
export function FleetStrip({
  running,
  stopped,
  broken,
  errors,
  topErrorProject,
  pulse,
  capacity,
  fallbackLastDeploy,
}: {
  running: number;
  stopped: number;
  broken: boolean;
  errors: number | undefined;
  topErrorProject: { name: string; count: number } | undefined;
  pulse: ProjectsPulse | undefined;
  capacity: FleetStripCapacity | undefined;
  fallbackLastDeploy: { name: string; at: string } | undefined;
}) {
  const requests = pulse?.requestsAvailable ? pulse.requests : undefined;
  const requestTotal = requests ? requests.reduce((n, [, v]) => n + v, 0) * ((pulse?.stepSeconds ?? 600) / 60) : undefined;
  const deploys = pulse?.deploys ?? [];
  const last = deploys[0] ? { name: deploys[0].project, at: deploys[0].at } : fallbackLastDeploy;
  const since = pulse ? Date.parse(pulse.since) : Date.now() - 24 * 3600 * 1000;

  return (
    <div className="mb-8 grid grid-cols-2 overflow-hidden rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-secondary)] sm:grid-cols-3 lg:flex lg:items-stretch lg:[&>*:not(:last-child)]:border-r lg:[&>*]:border-[var(--border-subtle)]">
      <div className={cell}>
        <span
          aria-hidden
          className={cn(
            "size-2 shrink-0 rounded-full",
            broken ? "bg-[var(--danger)]" : "bg-[var(--success)] shadow-[0_0_0_4px_var(--success-subtle)]",
          )}
        />
        <div className="min-w-0">
          <div className={big}>
            {running} <span className={unit}>running</span>
          </div>
          <div className={small}>{stopped} stopped</div>
        </div>
      </div>

      {requests && requestTotal !== undefined && (
        <Link href="/insights?m=requests" className={cn(cell, cellLink)}>
          <div className="min-w-0">
            <div className={big}>{compact(requestTotal)}</div>
            <div className={small}>requests, last 24h</div>
          </div>
          <Sparkline points={requests} className="hidden h-7 w-24 shrink-0 sm:block" />
        </Link>
      )}

      <div className={cell}>
        <div className="min-w-0">
          <div
            className={cn(
              big,
              errors === 0 && "text-[var(--success)]",
              errors !== undefined && errors > 0 && "text-[var(--danger)]",
            )}
          >
            {errors === undefined ? "—" : errors.toLocaleString()}
          </div>
          <div className={small}>
            {errors && topErrorProject ? (
              <>
                errors, {topErrorProject.count === errors ? "all" : "most"} from{" "}
                <Link href={`/projects/${topErrorProject.name}`} className={inlineLink}>
                  {topErrorProject.name}
                </Link>
              </>
            ) : (
              "errors, last 24h"
            )}
          </div>
        </div>
      </div>

      {capacity && capacity.memUsed > 0 && (
        <Link href="/insights?m=memory" className={cn(cell, cellLink)}>
          <div className="min-w-0">
            <div className={big}>
              {formatBytes(capacity.memUsed)}
              {capacity.memTotal ? <span className={unit}> / {formatBytes(capacity.memTotal)}</span> : null}
            </div>
            <div className={small}>
              {capacity.label}, CPU {formatCpu(capacity.cpuUsedMilli)}
              {capacity.cpuTotalMilli ? ` of ${capacity.cpuTotalMilli / 1000}` : ""}
            </div>
          </div>
        </Link>
      )}

      <div className={cn(cell, "col-span-2 sm:col-span-1 lg:flex-1")}>
        <div className="min-w-0 shrink-0">
          <div className={big}>{pulse ? deploys.length : last ? relativeTime(last.at) : "—"}</div>
          <div className={small}>
            {pulse ? "deploys" : "since the last deploy"}
            {last && (
              <>
                {" "}
                · {pulse ? "last " : ""}
                <Link href={`/projects/${last.name}`} className={inlineLink}>
                  {last.name}
                </Link>
                {pulse ? ` ${relativeTime(last.at)}` : ""}
              </>
            )}
          </div>
        </div>
        {deploys.length > 0 && (
          <DeployTicks
            times={deploys.map((d) => Date.parse(d.at))}
            since={since}
            className="hidden h-7 min-w-16 flex-1 lg:block"
          />
        )}
      </div>

      <Link href="/insights" className={cn(cell, cellLink, "col-span-2 text-[var(--text-primary)] sm:col-span-1 lg:shrink-0")}>
        <ChartLine className="size-4 shrink-0 text-[var(--accent)]" aria-hidden />
        <div className="min-w-0">
          <div className="text-[13px] font-medium">Insights</div>
          <div className={small}>compare services</div>
        </div>
        <ArrowRight className="ml-auto size-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
      </Link>
    </div>
  );
}

// Only shown when something is broken or rolling out, as redline notes
// on the drawing: each line opens that service.
export function ProblemList({ items }: { items: ReadonlyArray<AttentionItem> }) {
  if (items.length === 0) return null;
  const shown = items.slice(0, MAX_PROBLEMS);
  const broken = items.some((a) => a.tone === "danger" || a.tone === "warning");
  return (
    <div
      className={cn(
        "mb-8 overflow-hidden rounded-lg border",
        broken ? "border-[var(--danger)]/35 bg-[var(--danger-subtle)]" : "border-[var(--building)]/35 bg-[var(--building-subtle)]",
      )}
    >
      <ul className="divide-y divide-[var(--border-subtle)]" aria-label="Services that need attention">
        {shown.map((a) => {
          const meta = SERVICE_STATUS[a.status];
          return (
            <li key={`${a.project}/${a.service}`}>
              <Link
                href={`/projects/${a.project}?service=${encodeURIComponent(a.service)}`}
                className="flex min-w-0 items-center gap-3 bg-[var(--bg-elevated)]/70 px-4 py-2.5 text-[13px] outline-none hover:bg-[var(--bg-elevated)] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)]"
              >
                <span aria-hidden className={cn("size-2 shrink-0 rounded-full", TONE_CLASSES[a.tone].dot, meta.pulse && "motion-safe:animate-pulse")} />
                <span className="shrink-0 font-medium text-[var(--text-primary)]">
                  {a.project}
                  <span className="px-1 text-[var(--text-tertiary)]">/</span>
                  {a.service}
                </span>
                <span className={cn("shrink-0 font-medium", TONE_CLASSES[a.tone].text)}>{meta.label}</span>
                {a.detail && <span className="min-w-0 truncate text-[var(--text-secondary)]">{a.detail}</span>}
                <span aria-hidden className="ml-auto shrink-0 text-xs text-[var(--text-tertiary)]">
                  Open
                </span>
              </Link>
            </li>
          );
        })}
      </ul>
      {items.length > shown.length && (
        <p className="border-t border-[var(--border-subtle)] px-4 py-2 text-xs text-[var(--text-secondary)]">
          {items.length - shown.length} more, marked on the sheets below
        </p>
      )}
    </div>
  );
}
