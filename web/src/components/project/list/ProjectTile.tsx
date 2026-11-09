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
import { tileSpan } from "./fleetModel";
import { formatBytes, type ProjectCardModel } from "./projectCardModel";

const RAIL: Record<StatusTone, string> = {
  success: "bg-[var(--success)]",
  progress: "bg-[var(--building)] motion-safe:animate-pulse",
  warning: "bg-[var(--warning)]",
  danger: "bg-[var(--danger)]",
  info: "bg-[var(--border-strong)]",
  neutral: "bg-[var(--border-strong)]",
};

const MAX_PILLS = 8;

function compact(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k` : String(n);
}

// One pill per service: what it's called, what state it's in and how
// many errors it logged in 24h. Each opens that service on the canvas.
function ServicePills({ model, errors }: { model: ProjectCardModel; errors: ProjectErrorSummary | undefined }) {
  if (!model.hasSummary) {
    return (
      <div className="flex gap-1.5">
        <Skeleton className="h-7 w-20 rounded-md" />
        <Skeleton className="h-7 w-24 rounded-md" />
      </div>
    );
  }
  if (model.services.length === 0) {
    return <p className="text-[13px] text-[var(--text-tertiary)]">No services yet. Open the project to add one.</p>;
  }
  const shown = model.services.slice(0, MAX_PILLS);
  const hidden = model.services.slice(MAX_PILLS);
  return (
    <ul className="flex flex-wrap gap-1.5" aria-label={`Services in ${model.name}`}>
      {shown.map((s) => {
        const meta = SERVICE_STATUS[s.status];
        const errs = errors?.services[s.name]?.occurrences ?? 0;
        const resting = s.status === "stopped" || s.status === "sleeping";
        const loud = meta.tone === "danger" || meta.tone === "warning";
        return (
          <li key={s.name} className="min-w-0">
            <Link
              href={`/projects/${model.name}?service=${encodeURIComponent(s.name)}`}
              title={[`${s.name}: ${meta.label}`, errs > 0 ? `${errs.toLocaleString()} errors in the last 24 hours` : "", s.detail ?? ""]
                .filter(Boolean)
                .join("\n")}
              aria-label={`${s.name}, ${meta.label}${errs > 0 ? `, ${errs} errors in the last 24 hours` : ""}`}
              className={cn(
                "relative z-30 flex h-7 max-w-[13rem] items-center gap-1.5 rounded-md border pl-2 text-[12.5px] outline-none transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
                errs > 0 ? "pr-0" : "pr-2",
                loud ? TONE_CLASSES[meta.tone].border : "border-[var(--border-subtle)]",
                resting
                  ? "bg-transparent text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                  : "bg-[var(--bg-secondary)] text-[var(--text-primary)] hover:border-[var(--border-strong)]",
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
                  className="ml-0.5 flex h-full shrink-0 items-center rounded-r-[5px] border-l border-[var(--border-subtle)] bg-[var(--danger-subtle)] px-1.5 tabular-nums text-[var(--danger)]"
                >
                  {compact(errs)}
                </span>
              )}
            </Link>
          </li>
        );
      })}
      {hidden.length > 0 && (
        <li
          className="flex h-7 items-center px-1 text-[12.5px] text-[var(--text-tertiary)]"
          title={hidden.map((s) => `${s.name}: ${SERVICE_STATUS[s.status].label}`).join("\n")}
        >
          and {hidden.length} more
        </li>
      )}
    </ul>
  );
}

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
  const when = stopped
    ? "Stopped"
    : model.lastDeployAt
      ? `Deployed ${relativeTime(model.lastDeployAt)}`
      : model.hasSummary && model.serviceCount > 0
        ? "Never deployed"
        : null;

  return (
    <li
      data-roving-cell
      onContextMenu={onContextMenu}
      className={cn("min-w-0", tileSpan(model.serviceCount) === 2 && "sm:col-span-2")}
    >
      <article
        className={cn(
          "group relative flex h-full flex-col gap-3 overflow-hidden rounded-lg border bg-[var(--bg-elevated)] py-3.5 pl-5 pr-3.5 transition-colors",
          "border-[var(--border-subtle)] hover:border-[var(--border-strong)]",
          "has-[a[data-card-link]:focus-visible]:border-[var(--ring)] has-[a[data-card-link]:focus-visible]:ring-2 has-[a[data-card-link]:focus-visible]:ring-[var(--ring)]/40",
          stopped && "bg-[var(--bg-primary)]",
        )}
      >
        <span aria-hidden className={cn("absolute inset-y-3 left-0 w-[3px] rounded-r-full", RAIL[model.hasSummary ? model.tone : "neutral"])} />
        <Link
          href={`/projects/${model.name}`}
          data-card-link
          data-roving-item
          aria-label={`Open project ${model.name}`}
          className="absolute inset-0 z-10 outline-none"
        />

        <header className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <h3
              className={cn(
                "flex items-center gap-1.5 truncate font-display text-[17px] font-semibold leading-tight [font-stretch:104%]",
                stopped ? "text-[var(--text-secondary)]" : "text-[var(--text-primary)]",
              )}
              title={model.description ? `${model.name}: ${model.description}` : undefined}
            >
              <span className="truncate">{model.name}</span>
              {starred && <Star aria-label="Starred" className="hidden size-3 shrink-0 fill-current text-[var(--warning)] sm:block" />}
            </h3>
            {domain ? (
              <a
                href={domain.href}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={`Visit ${domain.host} (opens in new tab)`}
                className="relative z-30 mt-0.5 block w-fit max-w-full truncate rounded-sm text-[13px] text-[var(--text-tertiary)] outline-none hover:text-[var(--text-primary)] hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
              >
                {domain.host}
              </a>
            ) : (
              <p className="mt-0.5 text-[13px] text-[var(--text-tertiary)]">No public address</p>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {when && (
              <span className="whitespace-nowrap text-xs text-[var(--text-tertiary)]" title={model.lastDeployAt}>
                {when}
              </span>
            )}
            <div className="relative z-30 -mr-1.5 flex items-center">
              <button
                type="button"
                aria-label={starred ? `Unstar ${model.name}` : `Star ${model.name}`}
                aria-pressed={starred}
                title={starred ? "Unstar" : "Star"}
                onClick={onToggleStar}
                className="inline-flex size-7 items-center justify-center rounded-md text-[var(--text-tertiary)] outline-none hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)] sm:hidden sm:group-hover:inline-flex sm:group-has-[:focus-visible]:inline-flex"
              >
                <Star className={cn("size-3.5", starred && "fill-current text-[var(--warning)]")} />
              </button>
              <ProjectCardMenu model={model} actions={actions} />
            </div>
          </div>
        </header>

        <div className="relative z-20 flex-1">
          <ServicePills model={model} errors={errors} />
        </div>

        {metrics && (
          <p
            className="text-xs tabular-nums text-[var(--text-tertiary)]"
            title={`${formatCpu(metrics.cpuMillicores)} CPU and ${formatBytes(metrics.memBytes)} memory across ${metrics.pods} pod${metrics.pods === 1 ? "" : "s"}`}
          >
            {formatBytes(metrics.memBytes)} memory and {formatCpu(metrics.cpuMillicores)}
          </p>
        )}
      </article>
    </li>
  );
}

export function ProjectTileSkeleton({ wide = false }: { wide?: boolean }) {
  return (
    <li className={cn("rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4", wide && "sm:col-span-2")}>
      <Skeleton className="h-4 w-28 rounded" />
      <Skeleton className="mt-2 h-3 w-36 rounded" />
      <div className="mt-4 flex gap-1.5">
        <Skeleton className="h-7 w-20 rounded-md" />
        <Skeleton className="h-7 w-24 rounded-md" />
      </div>
    </li>
  );
}
