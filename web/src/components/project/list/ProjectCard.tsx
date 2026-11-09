"use client";

import Link from "next/link";
import { AlertCircle, GitBranch, Globe, Star } from "lucide-react";
import { errorCountLabel, useProjectErrorCounts } from "@/features/errors";
import { useLiveProjectDomain } from "@/features/projects/liveDomain";
import { Skeleton } from "@/components/ui/skeleton";
import { StatusChip } from "@/components/ui/status-chip";
import { formatCpu, relativeTime } from "@/lib/format";
import type { StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { ProjectCardMenu, type ProjectCardActions } from "./ProjectCardMenu";
import { ProjectStatusBadge } from "./ProjectStatusBadge";
import { formatBytes, type ProjectCardModel } from "./projectCardModel";

// Left rail in the card's worst service tone; settled-neutral cards get none.
const TONE_RAIL: Record<StatusTone, string> = {
  success: "before:bg-[var(--success)]",
  progress: "before:bg-[var(--building)]",
  warning: "before:bg-[var(--warning)]",
  danger: "before:bg-[var(--danger)]",
  info: "before:bg-transparent",
  neutral: "before:bg-transparent",
};

function ExternalRow({ icon: Icon, text, href, label }: {
  icon: React.ComponentType<{ className?: string }>;
  text: string;
  href?: string;
  label: string;
}) {
  const inner = (
    <>
      <Icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
      <span className="truncate font-mono text-xs">{text}</span>
    </>
  );
  if (!href) {
    return <div className="flex min-w-0 items-center gap-2 text-[var(--text-secondary)]">{inner}</div>;
  }
  // Sits above the card's overlay link so it opens the real site/repo.
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={`${label}: ${text} (opens in new tab)`}
      className="relative z-30 flex w-fit max-w-full min-w-0 items-center gap-2 rounded-sm text-[var(--text-secondary)] outline-none hover:text-[var(--text-primary)] hover:underline focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
    >
      {inner}
    </a>
  );
}

const NO_SERVICES: readonly string[] = [];

// Log errors over 24h, from the same per-project summary the canvas
// badges read (shared cache key). Separate from the Failing status, which
// is rollout health: a live service can still log errors.
function ErrorsChip({ project }: { project: string }) {
  const { total, unavailable } = useProjectErrorCounts(project, NO_SERVICES);
  // ProjectsList says "unavailable" once for the page, not per card.
  if (unavailable || !total) return null;
  const label = errorCountLabel(total);
  return (
    <StatusChip tone="danger" icon={AlertCircle} title={`${label}: logged in the last 24 hours`}>
      {label}
    </StatusChip>
  );
}

export function ProjectCard({
  model,
  starred,
  onToggleStar,
  actions,
  onContextMenu,
}: {
  model: ProjectCardModel;
  starred: boolean;
  onToggleStar: () => void;
  actions: ProjectCardActions;
  onContextMenu: (e: React.MouseEvent) => void;
}) {
  const deployed = model.lastDeployAt ? relativeTime(model.lastDeployAt) : null;
  const metrics = model.metrics && model.metrics.pods > 0 ? model.metrics : null;
  const domain = useLiveProjectDomain(model.name, model.primaryDomain);

  return (
    <li className="h-full" data-roving-cell onContextMenu={onContextMenu}>
      <div
        className={cn(
          // Same card recipe as the canvas nodes: lit surface, depth, and
          // the status LED rail along the top edge.
          "group relative flex h-full flex-col rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] p-4 shadow-node transition-[border-color,box-shadow]",
          "hover:border-[var(--border-strong)] hover:shadow-node-hover has-[a[data-card-link]:focus-visible]:border-[var(--ring)] has-[a[data-card-link]:focus-visible]:ring-2 has-[a[data-card-link]:focus-visible]:ring-[var(--ring)]/40",
          "before:absolute before:inset-x-4 before:top-0 before:h-[2px] before:rounded-b-full",
          TONE_RAIL[model.tone],
        )}
      >
        <Link
          href={`/projects/${model.name}`}
          data-card-link
          data-roving-item
          aria-label={`Open project ${model.name}`}
          className="absolute inset-0 z-10 rounded-xl outline-none"
        />
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <h2 className="truncate font-display text-base font-semibold [font-stretch:104%] text-[var(--text-primary)]">
              {model.name}
            </h2>
            <div className="mt-1 h-4">
              {model.hasSummary ? (
                <ProjectStatusBadge model={model} />
              ) : (
                <Skeleton className="h-3.5 w-24 rounded" />
              )}
            </div>
          </div>
          <div className="relative z-30 -mr-1 -mt-1 flex shrink-0 items-center">
            <button
              type="button"
              aria-label={starred ? `Unstar ${model.name}` : `Star ${model.name}`}
              aria-pressed={starred}
              title={starred ? "Unstar" : "Star"}
              onClick={onToggleStar}
              className={cn(
                "inline-flex h-7 w-7 items-center justify-center rounded-md outline-none hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
                starred
                  ? "text-[var(--warning)]"
                  : "text-[var(--text-tertiary)] opacity-100 hover:text-[var(--text-primary)] sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 sm:group-has-[:focus-visible]:opacity-100",
              )}
            >
              <Star className={cn("h-4 w-4", starred && "fill-current")} />
            </button>
            <ProjectCardMenu model={model} actions={actions} />
          </div>
        </div>

        {model.description && (
          <p className="mt-2 line-clamp-2 text-[13px] leading-snug text-[var(--text-secondary)]">
            {model.description}
          </p>
        )}

        <div className="mt-3 space-y-1.5">
          {domain && <ExternalRow icon={Globe} text={domain.host} href={domain.href} label="Site" />}
          {model.repo && <ExternalRow icon={GitBranch} text={model.repo} href={model.repoHref} label="Repository" />}
        </div>

        <div aria-hidden className="min-h-3 flex-1" />
        {/* Two fixed columns: facts wrap on the left, usage stays one unit on the right. */}
        <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 border-t border-[var(--border-subtle)] pt-3 text-xs text-[var(--text-tertiary)]">
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            <span>
              {deployed ? (
                <>Deployed {deployed}</>
              ) : model.hasSummary && model.serviceCount > 0 ? (
                "No deploys yet"
              ) : model.createdAt ? (
                <>Created {relativeTime(model.createdAt)}</>
              ) : null}
            </span>
            {model.serviceCount > 0 && (
              <span>
                {model.serviceCount} service{model.serviceCount === 1 ? "" : "s"}
              </span>
            )}
            {model.serviceCount > 0 && <ErrorsChip project={model.name} />}
          </div>
          {metrics && (
            <span
              className="whitespace-nowrap tabular-nums"
              title={`${formatCpu(metrics.cpuMillicores)} CPU, ${formatBytes(metrics.memBytes)} memory across ${metrics.pods} service pod${metrics.pods === 1 ? "" : "s"}`}
            >
              {formatCpu(metrics.cpuMillicores)} · {formatBytes(metrics.memBytes)}
            </span>
          )}
        </div>
      </div>
    </li>
  );
}

export function ProjectCardSkeleton() {
  return (
    <li className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4 shadow-node">
      <Skeleton className="h-4 w-32 rounded" />
      <Skeleton className="mt-2 h-3.5 w-24 rounded" />
      <Skeleton className="mt-4 h-3 w-48 rounded" />
      <Skeleton className="mt-2 h-3 w-40 rounded" />
      <div className="mt-4 border-t border-[var(--border-subtle)] pt-3">
        <Skeleton className="h-3 w-36 rounded" />
      </div>
    </li>
  );
}
