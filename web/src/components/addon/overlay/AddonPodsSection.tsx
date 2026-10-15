"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { LoadingState } from "@/components/ui/loading-state";
import { useAddonPods } from "@/features/pods";
import { cn } from "@/lib/utils";

// AddonPodsSection lists the datastore pod(s) plus any sidecar workload
// (e.g. the PgBouncer pooler). An empty list is a real signal: the chart
// rendered a workload with nothing running behind its Service.
export function AddonPodsSection({ project, addon }: { project: string; addon: string }) {
  const pods = useAddonPods(project, addon);
  const list = pods.data?.pods ?? [];

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">Pods</h3>
        {pods.data && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{pods.data.namespace}</span>
        )}
      </header>
      {pods.isPending ? (
        <LoadingState kind="inline" className="px-3 py-3" />
      ) : pods.isError ? (
        <QueryErrorState className="m-3" what="addon pods" error={pods.error} onRetry={() => void pods.refetch()} />
      ) : list.length === 0 ? (
        <p className="px-3 py-3 font-mono text-[11px] text-[var(--warning)]">
          No pods running — connections to this addon will be refused until one starts.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)]">
          {list.map((p) => (
            <li key={p.name} className="space-y-0.5 px-3 py-2 font-mono text-[11px]">
              <div className="flex items-center gap-2">
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
                {p.component && <span className="text-[var(--text-tertiary)]">{p.component}</span>}
                {(p.restarts ?? 0) > 0 && (
                  <span className="text-[var(--warning)]" title="container restarts">
                    {p.restarts}×
                  </span>
                )}
                <span className={p.ready ? "text-[var(--text-secondary)]" : "text-[var(--warning)]"}>
                  {p.ready ? "ready" : (p.phase || "not ready").toLowerCase()}
                </span>
              </div>
              {p.message && <p className="break-words pl-3.5 text-[var(--warning)]">{p.message}</p>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
