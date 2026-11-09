"use client";

import Link from "next/link";
import { Star } from "lucide-react";
import { useLiveProjectDomain } from "@/features/projects/liveDomain";
import type { ProjectErrorSummary } from "@/features/services/api";
import { Skeleton } from "@/components/ui/skeleton";
import { DraftingGrid } from "@/components/layout/DraftingGrid";
import { formatCpu, relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";
import { ProjectCardMenu, type ProjectCardActions } from "./ProjectCardMenu";
import { projectErrorTotal } from "./fleetModel";
import { formatBytes, type ProjectCardModel, type ServiceTile } from "./projectCardModel";

// Every sheet is the same size whatever the project holds: the plate
// shows at most this many service nodes and folds the rest into "+N".
const MAX_NODES = 6;

function compact(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k` : String(n);
}

// One node per service, drawn the way the project canvas draws it.
function ServiceNode({ project, s, errs }: { project: string; s: ServiceTile; errs: number }) {
  const meta = SERVICE_STATUS[s.status];
  const resting = s.status === "stopped" || s.status === "sleeping";
  const loud = meta.tone === "danger" || meta.tone === "warning";
  return (
    <li className="min-w-0">
      <Link
        href={`/projects/${project}?service=${encodeURIComponent(s.name)}`}
        title={[`${s.name}: ${meta.label}`, errs > 0 ? `${errs.toLocaleString()} errors in 24h` : "", s.detail ?? ""]
          .filter(Boolean)
          .join("\n")}
        aria-label={`${s.name}, ${meta.label}${errs > 0 ? `, ${errs} errors in the last 24 hours` : ""}`}
        className={cn(
          "relative z-30 flex h-6 max-w-[9.5rem] items-center gap-1.5 overflow-hidden rounded-[5px] border pl-1.5 text-xs shadow-node outline-none transition-[border-color,box-shadow] focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
          errs > 0 ? "pr-0" : "pr-2",
          loud ? TONE_CLASSES[meta.tone].border : "border-[var(--border-default)]",
          resting
            ? "border-dashed bg-[var(--canvas-bg)] text-[var(--text-tertiary)] shadow-none"
            : "bg-[var(--bg-elevated)] text-[var(--text-primary)] hover:border-[var(--border-strong)] hover:shadow-node-hover",
        )}
      >
        <span
          aria-hidden
          className={cn(
            "size-1.5 shrink-0 rounded-full",
            resting ? "border border-[var(--text-tertiary)]" : TONE_CLASSES[meta.tone].dot,
            meta.pulse && "motion-safe:animate-pulse",
          )}
        />
        <span className="truncate">{s.name}</span>
        {errs > 0 && (
          <span
            aria-hidden
            className="ml-0.5 flex h-full shrink-0 items-center border-l border-[var(--danger)]/30 bg-[var(--danger-subtle)] px-1 text-[11px] tabular-nums text-[var(--danger)]"
          >
            {compact(errs)}
          </span>
        )}
      </Link>
    </li>
  );
}

// The plate: a scrap of the project canvas with its services on it.
function Plate({ model, errors }: { model: ProjectCardModel; errors: ProjectErrorSummary | undefined }) {
  const stopped = model.status === "stopped";
  const shown = model.services.slice(0, MAX_NODES);
  const hidden = model.services.slice(MAX_NODES);
  return (
    <div
      className={cn(
        "relative isolate h-[7.5rem] shrink-0 overflow-hidden border-b border-[var(--border-subtle)] bg-[var(--canvas-bg)]",
        stopped && "opacity-70",
      )}
    >
      <DraftingGrid className="-z-10 opacity-70" />
      <div className="relative z-20 flex h-full items-center justify-center px-4 pt-3 pb-3">
        {!model.hasSummary ? (
          <div className="flex gap-2">
            <Skeleton className="h-6 w-20 rounded-[5px]" />
            <Skeleton className="h-6 w-16 rounded-[5px]" />
          </div>
        ) : model.services.length === 0 ? (
          <p className="text-xs text-[var(--text-tertiary)]">Empty canvas. Open it to add a service.</p>
        ) : (
          <ul
            aria-label={`Services in ${model.name}`}
            className="flex max-h-full flex-wrap content-center items-center justify-center gap-x-2.5 gap-y-3 overflow-hidden"
          >
            {shown.map((s) => (
              <ServiceNode key={s.name} project={model.name} s={s} errs={errors?.services[s.name]?.occurrences ?? 0} />
            ))}
            {hidden.length > 0 && (
              <li
                className="flex h-6 items-center rounded-[5px] bg-[var(--bg-tertiary)] px-1.5 text-[11px] tabular-nums text-[var(--text-secondary)]"
                title={hidden.map((s) => `${s.name}: ${SERVICE_STATUS[s.status].label}`).join("\n")}
              >
                +{hidden.length}
              </li>
            )}
          </ul>
        )}
      </div>
    </div>
  );
}

// ProjectTile is a drawing sheet: the plate on top, a ruled title block
// below. Fixed height, so a wall of them lines up on every row.
export function ProjectTile({
  model,
  errors,
  starred,
  onToggleStar,
  actions,
  onContextMenu,
}: {
  model: ProjectCardModel;
  errors: ProjectErrorSummary | undefined;
  starred: boolean;
  onToggleStar: () => void;
  actions: ProjectCardActions;
  onContextMenu: (e: React.MouseEvent) => void;
}) {
  const domain = useLiveProjectDomain(model.name, model.primaryDomain);
  const metrics = model.metrics && model.metrics.pods > 0 ? model.metrics : null;
  const stopped = model.status === "stopped";
  const errTotal = projectErrorTotal(errors);
  const when = stopped
    ? "Stopped"
    : model.lastDeployAt
      ? relativeTime(model.lastDeployAt)
      : model.hasSummary && model.serviceCount > 0
        ? "Never"
        : "—";

  return (
    <li data-roving-cell onContextMenu={onContextMenu} className="min-w-0">
      <article
        className={cn(
          "group relative flex h-[13.75rem] flex-col overflow-hidden rounded-lg border bg-[var(--bg-elevated)] transition-[border-color,box-shadow]",
          "border-[var(--border-default)] hover:border-[var(--border-strong)] hover:shadow-node-hover",
          "has-[a[data-card-link]:focus-visible]:border-[var(--ring)] has-[a[data-card-link]:focus-visible]:ring-2 has-[a[data-card-link]:focus-visible]:ring-[var(--ring)]/40",
        )}
      >
        <Link
          href={`/projects/${model.name}`}
          data-card-link
          data-roving-item
          aria-label={`Open project ${model.name}`}
          className="absolute inset-0 z-10 outline-none"
        />

        <Plate model={model} errors={errors} />

        {/* Title block */}
        <div className="flex min-h-0 flex-1 flex-col">
          <header className="flex min-w-0 flex-1 items-center gap-2 px-3.5">
            <span
              aria-hidden
              className={cn(
                "size-2 shrink-0 rounded-full",
                model.hasSummary ? TONE_CLASSES[model.tone].dot : "bg-[var(--border-strong)]",
                stopped && "bg-transparent ring-1 ring-[var(--text-tertiary)]",
                model.tone === "progress" && "motion-safe:animate-pulse",
              )}
            />
            <div className="min-w-0 flex-1">
              <h3
                className={cn(
                  "flex items-center gap-1.5 font-display text-[15px] font-semibold leading-tight",
                  stopped ? "text-[var(--text-secondary)]" : "text-[var(--text-primary)]",
                )}
                title={model.description ? `${model.name}: ${model.description}` : undefined}
              >
                <span className="truncate">{model.name}</span>
                {starred && <Star aria-label="Starred" className="hidden size-3 shrink-0 sm:block fill-current text-[var(--warning)]" />}
              </h3>
              {domain ? (
                <a
                  href={domain.href}
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label={`Visit ${domain.host} (opens in new tab)`}
                  className="relative z-30 block w-fit max-w-full truncate rounded-sm font-mono text-[11.5px] text-[var(--text-tertiary)] outline-none hover:text-[var(--link)] hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                >
                  {domain.host}
                </a>
              ) : (
                <p className="text-[11.5px] text-[var(--text-tertiary)]">Internal only</p>
              )}
            </div>
            <div className="relative z-30 -mr-1.5 flex shrink-0 items-center">
              <button
                type="button"
                aria-label={starred ? `Unstar ${model.name}` : `Star ${model.name}`}
                aria-pressed={starred}
                title={starred ? "Unstar" : "Star"}
                onClick={onToggleStar}
                className="inline-flex size-7 items-center justify-center rounded-md text-[var(--text-tertiary)] outline-none hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)] sm:opacity-0 sm:group-hover:opacity-100 sm:group-has-[:focus-visible]:opacity-100"
              >
                <Star className={cn("size-3.5", starred && "fill-current text-[var(--warning)]")} />
              </button>
              <ProjectCardMenu model={model} actions={actions} />
            </div>
          </header>

          <dl className="grid h-10 shrink-0 grid-cols-3 border-t border-[var(--border-subtle)] text-xs tabular-nums">
            <Cell label="Deployed" title={model.lastDeployAt}>
              {when}
            </Cell>
            <Cell
              label="Errors 24h"
              tone={errTotal ? "danger" : undefined}
              title={errTotal === undefined ? "Error counts unavailable" : undefined}
            >
              {errTotal === undefined ? "—" : compact(errTotal)}
            </Cell>
            <Cell
              label="Memory"
              title={metrics ? `${formatCpu(metrics.cpuMillicores)} CPU across ${metrics.pods} pod${metrics.pods === 1 ? "" : "s"}` : undefined}
            >
              {metrics ? formatBytes(metrics.memBytes) : "—"}
            </Cell>
          </dl>
        </div>
      </article>
    </li>
  );
}

function Cell({
  label,
  tone,
  title,
  children,
}: {
  label: string;
  tone?: "danger";
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col justify-center border-l border-[var(--border-subtle)] px-3.5 first:border-l-0" title={title}>
      <dt className="text-[10.5px] leading-none text-[var(--text-tertiary)]">{label}</dt>
      <dd className={cn("mt-1 truncate leading-none", tone === "danger" ? "text-[var(--danger)]" : "text-[var(--text-primary)]")}>
        {children}
      </dd>
    </div>
  );
}

export function ProjectTileSkeleton() {
  return (
    <li className="flex h-[13.75rem] flex-col overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-elevated)]">
      <div className="h-[7.5rem] border-b border-[var(--border-subtle)] bg-[var(--canvas-bg)]" />
      <div className="flex-1 p-3.5">
        <Skeleton className="h-4 w-28 rounded" />
        <Skeleton className="mt-2 h-3 w-36 rounded" />
      </div>
    </li>
  );
}
