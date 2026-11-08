"use client";

import Link from "next/link";
import { AlertCircle, GitBranch, Globe, Star } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { getProjectErrorSummary, projectErrorSummaryQueryKey } from "@/features/services";
import { Skeleton } from "@/components/ui/skeleton";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { ProjectCardMenu, type ProjectCardActions } from "./ProjectCardMenu";
import { ProjectStatusBadge } from "./ProjectStatusBadge";
import { formatBytes, formatMillicores, type ProjectCardModel } from "./projectCardModel";

const STATUS_RAIL: Record<ProjectCardModel["status"], string> = {
  live: "before:bg-[var(--success)]",
  failing: "before:bg-[var(--error)]",
  building: "before:bg-[var(--building)]",
  sleeping: "before:bg-[var(--info)]",
  stopped: "before:bg-transparent",
  pending: "before:bg-transparent",
  empty: "before:bg-transparent",
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

// Log errors over 24h, from the same per-project summary the canvas
// badges read (shared cache key). Separate from the Failing status, which
// is rollout health: a live service can still log errors. Servers without
// the summary endpoint 404 once and the chip stays hidden.
function ErrorsChip({ project }: { project: string }) {
  const q = useQuery({
    queryKey: projectErrorSummaryQueryKey(project, "24h"),
    queryFn: () => getProjectErrorSummary(project, "24h"),
    staleTime: 30_000,
    refetchInterval: (query) => (query.state.status === "error" ? false : 60_000),
    refetchIntervalInBackground: false,
    retry: false,
  });
  const total = Object.values(q.data?.services ?? {}).reduce((n, s) => n + (s.occurrences || 0), 0);
  if (total <= 0) return null;
  const label = `${total} ${total === 1 ? "error" : "errors"} · 24h`;
  return (
    <span
      title={`${label.replace(" · 24h", "")} logged in the last 24 hours`}
      className="inline-flex h-5 shrink-0 items-center gap-1 rounded-md border border-[var(--error)]/35 bg-[var(--error-subtle)] px-1.5 text-2xs font-medium tabular-nums text-[var(--error)]"
    >
      <AlertCircle className="h-3 w-3" aria-hidden />
      {label}
    </span>
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

  return (
    <li className="h-full" onContextMenu={onContextMenu}>
      <div
        className={cn(
          "group relative flex h-full flex-col overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 transition-colors",
          "hover:border-[var(--border-strong)] has-[a[data-card-link]:focus-visible]:border-[var(--ring)] has-[a[data-card-link]:focus-visible]:ring-2 has-[a[data-card-link]:focus-visible]:ring-[var(--ring)]/40",
          "before:absolute before:inset-y-0 before:left-0 before:w-0.5",
          STATUS_RAIL[model.status],
        )}
      >
        <Link
          href={`/projects/${model.name}`}
          data-card-link
          aria-label={`Open project ${model.name}`}
          className="absolute inset-0 z-10 rounded-lg outline-none"
        />
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-[15px] font-semibold tracking-tight text-[var(--text-primary)]">
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
          {model.domain && <ExternalRow icon={Globe} text={model.domain} href={model.domainHref} label="Site" />}
          {model.repo && <ExternalRow icon={GitBranch} text={model.repo} href={model.repoHref} label="Repository" />}
        </div>

        <div aria-hidden className="min-h-3 flex-1" />
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-[var(--border-subtle)] pt-3 text-xs text-[var(--text-tertiary)]">
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
          {metrics && (
            <span
              className="ml-auto tabular-nums"
              title={`${formatMillicores(metrics.cpuMillicores)} CPU, ${formatBytes(metrics.memBytes)} memory across ${metrics.pods} pod${metrics.pods === 1 ? "" : "s"}`}
            >
              {metrics.cpuMillicores > 0 && <>{formatMillicores(metrics.cpuMillicores)} CPU · </>}
              {formatBytes(metrics.memBytes)}
            </span>
          )}
        </div>
      </div>
    </li>
  );
}

export function ProjectCardSkeleton() {
  return (
    <li className="rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
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
