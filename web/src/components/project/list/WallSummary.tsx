"use client";

import Link from "next/link";
import { ArrowRight, ChartLine, SlidersHorizontal } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { formatCpu, relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { ProjectsPulse } from "@/features/projects";
import type { AttentionItem } from "./fleetModel";
import { formatBytes } from "./projectCardModel";
import { DEFAULT_PANELS, PANELS, type FleetBackups, type FleetUptime, type FleetVulns, type PanelKey } from "./fleetPanels";

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

// Cells wrap and grow to fill each row. Every cell draws its right and
// bottom edge; the row box overhangs its frame by 1px so the outer edges
// are clipped.
const cell = "flex min-w-0 flex-[1_1_9.5rem] items-center gap-3 border-b border-r sm:flex-[1_1_13rem] border-[var(--border-subtle)] px-4 py-3 sm:px-5";
// Cells that open Insights on their metric.
const cellLink =
  "outline-none transition-colors hover:bg-[var(--bg-tertiary)] focus-visible:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)]";
const big = "whitespace-nowrap font-display text-[17px] font-semibold leading-tight text-[var(--text-primary)] [font-stretch:108%] tabular-nums";
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
  panels,
  onPanels,
  isAdmin,
  running,
  stopped,
  broken,
  errors,
  topErrorProject,
  pulse,
  capacity,
  fallbackLastDeploy,
  vulns,
  uptime,
  backups,
}: {
  panels: readonly PanelKey[];
  onPanels: (next: PanelKey[]) => void;
  isAdmin: boolean;
  running: number;
  stopped: number;
  broken: boolean;
  errors: number | undefined;
  topErrorProject: { name: string; count: number } | undefined;
  pulse: ProjectsPulse | undefined;
  capacity: FleetStripCapacity | undefined;
  fallbackLastDeploy: { name: string; at: string } | undefined;
  vulns: FleetVulns | undefined;
  uptime: FleetUptime | undefined;
  backups: FleetBackups | undefined;
}) {
  const requests = pulse?.requestsAvailable ? pulse.requests : undefined;
  const requestTotal = requests ? requests.reduce((n, [, v]) => n + v, 0) * ((pulse?.stepSeconds ?? 600) / 60) : undefined;
  const deploys = pulse?.deploys ?? [];
  const last = deploys[0] ? { name: deploys[0].project, at: deploys[0].at } : fallbackLastDeploy;
  const since = pulse ? Date.parse(pulse.since) : Date.now() - 24 * 3600 * 1000;
  const show = (k: PanelKey) => panels.includes(k) && (k !== "backups" || isAdmin);

  return (
    <div className="mb-8 flex overflow-hidden rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <div className="-mb-px -mr-px flex min-w-0 flex-1 flex-wrap">
      {show("running") && (
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
      )}

      {show("requests") && requests && requestTotal !== undefined && (
        <Link href="/insights?v=compare&m=requests" className={cn(cell, cellLink)}>
          <div className="min-w-0">
            <div className={big}>{compact(requestTotal)}</div>
            <div className={small}>requests, last 24h</div>
          </div>
          <Sparkline points={requests} className="hidden h-7 w-24 shrink-0 sm:block" />
        </Link>
      )}

      {show("errors") && (
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
      )}

      {show("cves") && <CvesCell vulns={vulns} />}

      {show("uptime") && (
        <div className={cell}>
          <span
            aria-hidden
            className={cn(
              "size-2 shrink-0 rounded-full",
              !uptime || !uptime.enabled || uptime.checked === 0
                ? "bg-[var(--text-tertiary)]"
                : uptime.bad.length > 0
                  ? "bg-[var(--danger)]"
                  : "bg-[var(--success)]",
            )}
          />
          <div className="min-w-0">
            <div className={big}>
              {uptime && uptime.enabled && uptime.checked > 0 ? (
                <>
                  {uptime.up}
                  <span className={unit}> / {uptime.checked} up</span>
                </>
              ) : (
                "—"
              )}
            </div>
            <div className={small}>
              {!uptime ? (
                "uptime checks"
              ) : !uptime.enabled ? (
                "uptime checks are off"
              ) : uptime.checked === 0 ? (
                "no uptime checks yet"
              ) : uptime.bad.length > 0 ? (
                <>
                  down:{" "}
                  <Link href={`/projects/${encodeURIComponent(uptime.bad[0].project)}?service=${encodeURIComponent(uptime.bad[0].service)}`} className={inlineLink}>
                    {uptime.bad[0].project === uptime.bad[0].service ? uptime.bad[0].project : `${uptime.bad[0].project} / ${uptime.bad[0].service}`}
                  </Link>
                  {uptime.bad.length > 1 ? ` +${uptime.bad.length - 1}` : ""}
                </>
              ) : (
                "every check passing"
              )}
            </div>
          </div>
        </div>
      )}

      {show("backups") && (
        <Link href="/settings/backups#addon-backups" className={cn(cell, cellLink)}>
          <div className="min-w-0">
            <div className={cn(big, backups && (backups.failing > 0 || !backups.controlPlaneHealthy) && "text-[var(--danger)]")}>
              {backups ? (
                <>
                  {backups.covered - backups.failing}
                  <span className={unit}> / {backups.total}</span>
                </>
              ) : (
                "—"
              )}
            </div>
            <div className={small}>
              {!backups
                ? "addon backups"
                : !backups.controlPlaneHealthy
                  ? "control-plane backup failing"
                  : backups.failing > 0
                    ? `addons backed up, ${backups.failing} failing`
                    : backups.uncovered > 0
                      ? `addons backed up, ${backups.uncovered} unscheduled`
                      : "addons backed up"}
            </div>
          </div>
        </Link>
      )}

      {show("capacity") && capacity && capacity.memUsed > 0 && (
        <Link href="/insights?v=compare&m=memory" className={cn(cell, cellLink)}>
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

      {show("deploys") && (
        <div className={cn(cell, "flex-[2_1_19rem] sm:flex-[2_1_26rem]")}>
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
              className="hidden h-7 min-w-16 flex-1 sm:block"
            />
          )}
        </div>
      )}

      {show("insights") && (
        <Link href="/insights" className={cn(cell, cellLink, "text-[var(--text-primary)]")}>
          <ChartLine className="size-4 shrink-0 text-[var(--accent)]" aria-hidden />
          <div className="min-w-0">
            <div className="text-[13px] font-medium">Insights</div>
            <div className={small}>cluster overview</div>
          </div>
          <ArrowRight className="ml-auto size-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
        </Link>
      )}

      </div>
      <PanelPicker panels={panels} onPanels={onPanels} isAdmin={isAdmin} />
    </div>
  );
}

// CVE totals for production, with staging and addons as a second line.
// The cell opens the worst production image's vulnerabilities.
function CvesCell({ vulns }: { vulns: FleetVulns | undefined }) {
  const prod = vulns?.production;
  const showCritical = !prod || prod.critical > 0;
  const n = prod ? (showCritical ? prod.critical : prod.high) : undefined;
  const clean = !!prod && prod.critical + prod.high === 0;
  const otherN = vulns ? vulns.other.critical : 0;
  const body = (
    <div className="min-w-0">
      <div className={cn(big, prod && prod.critical > 0 && "text-[var(--danger)]", clean && "text-[var(--success)]")}>
        {n === undefined ? "—" : n}
        <span className={unit}> {clean ? "CVEs in production" : showCritical ? "critical in production" : "high in production"}</span>
      </div>
      <div className={small}>
        {!vulns ? "CVEs in running images" : otherN > 0 ? `+${otherN} in staging & addons` : clean ? `none critical or high in ${prod?.images ?? 0} images` : "none in staging & addons"}
      </div>
    </div>
  );
  if (!vulns?.worst) return <div className={cell}>{body}</div>;
  return (
    <Link
      href={vulns.worst.href}
      title={`Worst: ${vulns.worst.label} (${vulns.worst.critical} critical, ${vulns.worst.high} high)`}
      className={cn(cell, cellLink)}
    >
      {body}
    </Link>
  );
}

function PanelPicker({ panels, onPanels, isAdmin }: { panels: readonly PanelKey[]; onPanels: (next: PanelKey[]) => void; isAdmin: boolean }) {
  const options = PANELS.filter((p) => !p.adminOnly || isAdmin);
  return (
    <Popover>
      <PopoverTrigger
        aria-label="Choose panels"
        title="Choose panels"
        className="flex w-11 shrink-0 items-center justify-center border-l border-[var(--border-subtle)] text-[var(--text-tertiary)] outline-none transition-colors hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)] data-[popup-open]:bg-[var(--bg-tertiary)] data-[popup-open]:text-[var(--text-primary)]"
      >
        <SlidersHorizontal className="size-4" aria-hidden />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 p-0">
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
          <p className="text-sm font-semibold tracking-tight">Panels</p>
          <button
            type="button"
            onClick={() => onPanels([...DEFAULT_PANELS])}
            className="inline-flex h-7 items-center rounded-md px-2 text-xs text-[var(--link)] outline-none hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
          >
            Reset
          </button>
        </header>
        <ul className="p-1">
          {options.map((p) => {
            const on = panels.includes(p.key);
            return (
              <li key={p.key}>
                <label className="flex cursor-pointer items-start gap-2.5 rounded-md px-2 py-1.5 hover:bg-[var(--bg-tertiary)] has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-[var(--ring)]">
                  <input
                    type="checkbox"
                    checked={on}
                    onChange={() => onPanels(on ? panels.filter((k) => k !== p.key) : [...panels, p.key])}
                    className="mt-0.5 accent-[var(--accent)]"
                  />
                  <span className="min-w-0">
                    <span className="block text-[13px] text-[var(--text-primary)]">{p.label}</span>
                    <span className="block text-xs text-[var(--text-tertiary)]">{p.hint}</span>
                  </span>
                </label>
              </li>
            );
          })}
        </ul>
      </PopoverContent>
    </Popover>
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
