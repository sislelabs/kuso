"use client";

import Link from "next/link";
import { Star } from "lucide-react";
import { useLiveProjectDomain } from "@/features/projects/liveDomain";
import type { ProjectErrorSummary } from "@/features/services/api";
import { Skeleton } from "@/components/ui/skeleton";
import { formatCpu, relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { ProjectCardMenu, type ProjectCardActions } from "./ProjectCardMenu";
import { ProjectStatusBadge } from "./ProjectStatusBadge";
import { projectErrorTotal } from "./fleetModel";
import { formatBytes, type ProjectCardModel } from "./projectCardModel";

// Shared by the column header and every row so they line up.
export const BOARD_COLS =
  "lg:grid lg:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_6.5rem_8.5rem_6rem_4rem] lg:items-center lg:gap-x-5";

const TONE_EDGE: Record<StatusTone, string> = {
  success: "before:bg-[var(--success)]",
  progress: "before:bg-[var(--building)]",
  warning: "before:bg-[var(--warning)]",
  danger: "before:bg-[var(--danger)]",
  info: "before:bg-[var(--text-tertiary)]",
  neutral: "before:bg-[var(--border-strong)]",
};

const MAX_TILES = 5;

function ServiceTiles({ model, errors }: { model: ProjectCardModel; errors: ProjectErrorSummary | undefined }) {
  if (!model.hasSummary) return <Skeleton className="h-6 w-40 rounded-md" />;
  if (model.services.length === 0) return <span className="text-xs text-[var(--text-tertiary)]">No services yet</span>;
  const shown = model.services.slice(0, MAX_TILES);
  const hidden = model.services.length - shown.length;
  return (
    <ul className="flex min-w-0 flex-wrap items-center gap-1.5" aria-label={`Services in ${model.name}`}>
      {shown.map((s) => {
        const meta = SERVICE_STATUS[s.status];
        const errs = errors?.services[s.name]?.occurrences ?? 0;
        const quiet = s.status === "stopped" || s.status === "sleeping";
        const title = [`${s.name}: ${meta.label}`, errs > 0 ? `${errs.toLocaleString()} errors in 24h` : null, s.detail]
          .filter(Boolean)
          .join("\n");
        return (
          <li key={s.name} className="min-w-0">
            <Link
              href={`/projects/${model.name}?service=${encodeURIComponent(s.name)}`}
              title={title}
              aria-label={`${s.name}, ${meta.label}${errs > 0 ? `, ${errs} errors in 24 hours` : ""}`}
              className={cn(
                "relative z-30 inline-flex h-6 max-w-[11rem] items-center gap-1.5 rounded-md border bg-[var(--bg-secondary)] px-2 font-mono text-[11px] outline-none transition-colors hover:border-[var(--border-strong)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
                meta.tone === "danger" || meta.tone === "warning"
                  ? TONE_CLASSES[meta.tone].border
                  : "border-[var(--border-subtle)]",
                quiet ? "text-[var(--text-tertiary)]" : "text-[var(--text-secondary)]",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "h-1.5 w-1.5 shrink-0 rounded-full",
                  TONE_CLASSES[meta.tone].dot,
                  meta.pulse && "motion-safe:animate-pulse",
                )}
              />
              <span className="truncate">{s.name}</span>
              {errs > 0 && (
                <span aria-hidden className="shrink-0 tabular-nums text-[var(--danger)]">
                  {errs > 999 ? `${Math.round(errs / 100) / 10}k` : errs}
                </span>
              )}
            </Link>
          </li>
        );
      })}
      {hidden > 0 && (
        <li
          className="text-[11px] text-[var(--text-tertiary)]"
          title={model.services
            .slice(MAX_TILES)
            .map((s) => `${s.name}: ${SERVICE_STATUS[s.status].label}`)
            .join("\n")}
        >
          +{hidden}
        </li>
      )}
    </ul>
  );
}

// A proportional hairline under a number: how this project compares with
// the biggest one on the board.
function Meter({ ratio, tone }: { ratio: number; tone: "danger" | "neutral" }) {
  const pct = Math.max(0, Math.min(1, ratio)) * 100;
  return (
    <span aria-hidden className="mt-1 ml-auto flex h-[3px] w-16 justify-end overflow-hidden rounded-full bg-[var(--bg-tertiary)]">
      <span
        className={cn("block h-full rounded-full", tone === "danger" ? "bg-[var(--danger)]/70" : "bg-[var(--text-tertiary)]")}
        style={{ width: `${pct > 0 ? Math.max(pct, 4) : 0}%` }}
      />
    </span>
  );
}

export function ProjectRow({
  model,
  errors,
  maxErrors,
  maxMem,
  starred,
  onToggleStar,
  actions,
  onContextMenu,
}: {
  model: ProjectCardModel;
  errors: ProjectErrorSummary | undefined;
  maxErrors: number;
  maxMem: number;
  starred: boolean;
  onToggleStar: () => void;
  actions: ProjectCardActions;
  onContextMenu: (e: React.MouseEvent) => void;
}) {
  const domain = useLiveProjectDomain(model.name, model.primaryDomain);
  const errTotal = model.serviceCount > 0 ? projectErrorTotal(errors) : undefined;
  const metrics = model.metrics && model.metrics.pods > 0 ? model.metrics : null;
  const deployed = model.lastDeployAt ? relativeTime(model.lastDeployAt) : null;
  const stopped = model.status === "stopped";

  return (
    <li
      data-roving-cell
      onContextMenu={onContextMenu}
      className={cn(
        "group relative grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-2 px-4 py-3 transition-colors hover:bg-[var(--bg-secondary)]",
        "has-[a[data-card-link]:focus-visible]:bg-[var(--bg-secondary)]",
        "before:absolute before:inset-y-3 before:left-0 before:w-[2px] before:rounded-r-full",
        TONE_EDGE[model.hasSummary ? model.tone : "neutral"],
        BOARD_COLS,
      )}
    >
      <Link
        href={`/projects/${model.name}`}
        data-card-link
        data-roving-item
        aria-label={`Open project ${model.name}`}
        className="absolute inset-0 z-10 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)]"
      />

      <div className="min-w-0">
        <div className="flex min-w-0 items-baseline gap-2">
          <h3
            className={cn(
              "truncate font-display text-[15px] font-semibold [font-stretch:104%]",
              stopped ? "text-[var(--text-secondary)]" : "text-[var(--text-primary)]",
            )}
            title={model.description ? `${model.name}: ${model.description}` : model.name}
          >
            {model.name}
          </h3>
          {starred && <Star aria-label="Starred" className="h-3 w-3 shrink-0 fill-current text-[var(--warning)]" />}
        </div>
        <div className="mt-0.5 flex h-4 min-w-0 items-center">
          {domain ? (
            <a
              href={domain.href}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={`Site: ${domain.host} (opens in new tab)`}
              className="relative z-30 truncate rounded-sm font-mono text-xs text-[var(--text-tertiary)] outline-none hover:text-[var(--text-primary)] hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
            >
              {domain.host}
            </a>
          ) : model.hasSummary ? (
            <ProjectStatusBadge model={model} className="text-[var(--text-tertiary)]" />
          ) : null}
        </div>
      </div>

      <div className="relative z-20 col-span-2 row-start-2 min-w-0 lg:col-span-1 lg:row-start-auto">
        <ServiceTiles model={model} errors={errors} />
      </div>

      {/* Mobile folds the three numeric columns into one line. */}
      <p className="col-span-2 row-start-3 flex flex-wrap gap-x-3 text-xs text-[var(--text-tertiary)] lg:hidden">
        {errTotal !== undefined && errTotal > 0 && (
          <span className="text-[var(--danger)]">{errTotal.toLocaleString()} errors · 24h</span>
        )}
        {metrics && (
          <span className="tabular-nums">
            {formatCpu(metrics.cpuMillicores)} · {formatBytes(metrics.memBytes)}
          </span>
        )}
        {deployed && <span>Deployed {deployed}</span>}
      </p>

      <div className="hidden text-right text-xs tabular-nums lg:block" title={errTotal ? `${errTotal.toLocaleString()} errors logged in the last 24 hours` : undefined}>
        {errTotal === undefined ? (
          <span className="text-[var(--text-tertiary)]">—</span>
        ) : errTotal === 0 ? (
          <span className="text-[var(--text-tertiary)]">0</span>
        ) : (
          <>
            <span className="font-medium text-[var(--danger)]">{errTotal.toLocaleString()}</span>
            <Meter ratio={maxErrors > 0 ? errTotal / maxErrors : 0} tone="danger" />
          </>
        )}
      </div>

      <div
        className="hidden text-right text-xs tabular-nums text-[var(--text-secondary)] lg:block"
        title={
          metrics
            ? `${formatCpu(metrics.cpuMillicores)} CPU, ${formatBytes(metrics.memBytes)} memory across ${metrics.pods} pod${metrics.pods === 1 ? "" : "s"}`
            : undefined
        }
      >
        {metrics ? (
          <>
            <span className="whitespace-nowrap">
              <span className="text-[var(--text-tertiary)]">{formatCpu(metrics.cpuMillicores)} · </span>
              {formatBytes(metrics.memBytes)}
            </span>
            <Meter ratio={maxMem > 0 ? metrics.memBytes / maxMem : 0} tone="neutral" />
          </>
        ) : (
          <span className="text-[var(--text-tertiary)]">{stopped ? "Stopped" : "—"}</span>
        )}
      </div>

      <div className="hidden text-right text-xs text-[var(--text-tertiary)] lg:block" title={model.lastDeployAt}>
        {deployed ?? (model.hasSummary && model.serviceCount > 0 ? "Never" : "—")}
      </div>

      <div className="relative z-30 col-start-2 row-start-1 -mr-1.5 flex items-center justify-end self-start lg:col-start-auto lg:row-start-auto lg:self-center">
        <button
          type="button"
          aria-label={starred ? `Unstar ${model.name}` : `Star ${model.name}`}
          aria-pressed={starred}
          title={starred ? "Unstar" : "Star"}
          onClick={onToggleStar}
          className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] opacity-100 outline-none hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)] sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 sm:group-has-[:focus-visible]:opacity-100"
        >
          <Star className={cn("h-3.5 w-3.5", starred && "fill-current text-[var(--warning)]")} />
        </button>
        <ProjectCardMenu model={model} actions={actions} />
      </div>
    </li>
  );
}

export function ProjectRowSkeleton() {
  return (
    <li className={cn("grid grid-cols-[minmax(0,1fr)_auto] gap-y-2 px-4 py-3.5", BOARD_COLS)}>
      <div>
        <Skeleton className="h-4 w-28 rounded" />
        <Skeleton className="mt-1.5 h-3 w-36 rounded" />
      </div>
      <Skeleton className="col-span-2 h-6 w-48 rounded-md lg:col-span-1" />
    </li>
  );
}
