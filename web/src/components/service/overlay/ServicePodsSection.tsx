"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { isCurrentPod, useServicePods } from "@/features/pods";
import { podServingSplit, rolloutInFlight } from "@/features/services";
import type { KusoEnvironment } from "@/types/projects";
import { cn } from "@/lib/utils";
import { NeverStartedState, useFirstRun } from "./firstRunContext";

// ServicePodsSection lists the pods backing one env of a service — the
// web counterpart of `kuso service pods`.
export function ServicePodsSection({
  project,
  service,
  envName,
  env,
}: {
  project: string;
  service: string;
  // Env CR name; undefined while the envs list loads or before a first deploy.
  envName?: string;
  env?: KusoEnvironment;
}) {
  const pods = useServicePods(project, service, envName);
  const { firstRun } = useFirstRun();
  // Old-ReplicaSet and release-hook pods are listed but don't count.
  const current = (pods.data?.pods ?? []).filter(isCurrentPod);
  const currentReady = current.filter((p) => p.ready).length;
  // Mid-rollout "0/1 ready" would hide the previous pod that still serves.
  const split =
    rolloutInFlight(env) && pods.data?.pods
      ? podServingSplit(pods.data.pods, (env?.status?.replicas as { desired?: number } | undefined)?.desired)
      : undefined;

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="text-xs font-medium text-[var(--text-tertiary)]">
          Pods
          {split ? (
            <span className="ml-1.5 font-normal">{split}</span>
          ) : current.length > 0 ? (
            <span className="ml-1.5 font-normal">
              {currentReady}/{current.length} ready
            </span>
          ) : null}
        </h3>
        {pods.data && (
          <span className="font-mono text-xs text-[var(--text-tertiary)]">{pods.data.namespace}</span>
        )}
      </header>
      {firstRun.kind === "never" ? (
        <NeverStartedState title="Nothing has run yet." compact />
      ) : !envName ? (
        <p className="px-3 py-3 text-xs text-[var(--text-tertiary)]">
          No environment deployed yet.
        </p>
      ) : pods.isPending ? (
        <div className="p-3">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : pods.isError ? (
        <QueryErrorState className="m-3" what="pods" error={pods.error} onRetry={() => void pods.refetch()} />
      ) : (pods.data.pods ?? []).length === 0 ? (
        <p className="px-3 py-3 text-xs text-[var(--text-tertiary)]">
          No pods running — the service is scaled to zero, asleep, or stopped.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)]">
          {(pods.data.pods ?? []).map((p) => (
            <li key={p.name} className={cn("flex items-center gap-2 px-3 py-2 font-mono text-xs", !isCurrentPod(p) && "opacity-60")}>
              <span
                aria-hidden
                className={cn(
                  "h-1.5 w-1.5 shrink-0 rounded-full",
                  !isCurrentPod(p)
                    ? "bg-[var(--text-tertiary)]"
                    : p.ready
                      ? "bg-[var(--success)]"
                      : p.phase === "Pending"
                        ? "bg-[var(--warning)]"
                        : "bg-[var(--error)]",
                )}
              />
              <span className="min-w-0 flex-1 truncate text-[var(--text-primary)]" title={p.name}>
                {p.name}
              </span>
              {(p.containers?.length ?? 0) > 1 && (
                <span className="hidden text-[var(--text-tertiary)] sm:inline" title={p.containers?.join(", ")}>
                  {p.containers?.length} containers
                </span>
              )}
              {!isCurrentPod(p) ? (
                <span className="text-[var(--text-tertiary)]" title="From the previous rollout or a release hook; not counted">
                  previous
                </span>
              ) : (
                <span className={p.ready ? "text-[var(--text-secondary)]" : "text-[var(--warning)]"}>
                  {p.ready ? "ready" : (p.phase || "not ready").toLowerCase()}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
