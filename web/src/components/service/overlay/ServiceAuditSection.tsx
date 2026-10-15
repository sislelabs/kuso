"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { useServiceAudit } from "@/features/service-audit";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// ServiceAuditSection is the per-service audit trail (`kuso audit --app`).
// Most actions record no env (phase), so it asks for every env ("*").
export function ServiceAuditSection({
  project,
  service,
}: {
  project: string;
  service: string;
}) {
  const audit = useServiceAudit(project, "*", service);
  const rows = audit.data?.audit ?? [];

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <h3 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          Audit trail
        </h3>
        {audit.data && audit.data.count > rows.length && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            latest {rows.length} of {audit.data.count}
          </span>
        )}
      </header>
      {audit.isPending ? (
        <div className="p-3">
          <Skeleton className="h-8 w-full" />
        </div>
      ) : audit.isError ? (
        <QueryErrorState className="m-3" what="audit trail" error={audit.error} onRetry={() => void audit.refetch()} />
      ) : rows.length === 0 ? (
        <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
          No audit entries for {service}.
        </p>
      ) : (
        <ul className="max-h-64 divide-y divide-[var(--border-subtle)] overflow-y-auto">
          {rows.map((e) => (
            <li key={e.id} className="space-y-0.5 px-3 py-2 text-[11px]">
              <div className="flex items-center gap-2 font-mono">
                <span
                  className={cn(
                    "text-[var(--text-primary)]",
                    e.severity === "warn" && "text-[var(--warning)]",
                    e.severity === "error" && "text-[var(--error)]",
                  )}
                >
                  {e.action}
                </span>
                <span className="text-[var(--text-tertiary)]">by {e.user || "system"}</span>
                <span className="ml-auto shrink-0 text-[var(--text-tertiary)]" title={e.timestamp}>
                  {relativeTime(e.timestamp)}
                </span>
              </div>
              {e.message && <p className="break-words text-[var(--text-secondary)]">{e.message}</p>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
