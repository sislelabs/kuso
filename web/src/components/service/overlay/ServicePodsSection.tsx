"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { useServicePods } from "@/features/pods";
import { cn } from "@/lib/utils";

// ServicePodsSection lists the pods backing one env of a service — the
// web counterpart of `kuso service pods`.
export function ServicePodsSection({
  project,
  service,
  envName,
}: {
  project: string;
  service: string;
  // Env CR name; undefined while the envs list loads or before a first deploy.
  envName?: string;
}) {
  const pods = useServicePods(project, service, envName);

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">Pods</h3>
        {pods.data && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{pods.data.namespace}</span>
        )}
      </header>
      {!envName ? (
        <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
          No environment deployed yet.
        </p>
      ) : pods.isPending ? (
        <div className="p-3">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : pods.isError ? (
        <QueryErrorState className="m-3" what="pods" error={pods.error} onRetry={() => void pods.refetch()} />
      ) : (pods.data.pods ?? []).length === 0 ? (
        <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
          No pods running — the service is scaled to zero, asleep, or stopped.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)]">
          {(pods.data.pods ?? []).map((p) => (
            <li key={p.name} className="flex items-center gap-2 px-3 py-2 font-mono text-[11px]">
              <span
                aria-hidden
                className={cn(
                  "h-1.5 w-1.5 shrink-0 rounded-full",
                  p.ready ? "bg-[var(--success)]" : p.phase === "Pending" ? "bg-[var(--warning)]" : "bg-[var(--error)]",
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
              <span className={p.ready ? "text-[var(--text-secondary)]" : "text-[var(--warning)]"}>
                {p.ready ? "ready" : (p.phase || "not ready").toLowerCase()}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
