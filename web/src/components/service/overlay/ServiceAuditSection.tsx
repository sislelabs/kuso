"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { useServiceAudit } from "@/features/service-audit";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { AuditMessage } from "@/components/activity/AuditMessage";
import { UserBadge } from "@/components/activity/UserBadge";
import { auditActionLabel } from "@/components/activity/audit-text";

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
        <h3 className="text-xs font-medium text-[var(--text-tertiary)]">
          Audit trail
        </h3>
        {audit.data && audit.data.count > rows.length && (
          <span className="font-mono text-xs text-[var(--text-tertiary)]">
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
        <p className="px-3 py-3 text-xs text-[var(--text-tertiary)]">
          No audit entries for {service}.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)]">
          {rows.map((e) => (
            <li key={e.id} className="space-y-0.5 px-3 py-2 text-xs">
              <div className="flex min-w-0 items-center gap-2">
                <span
                  title={e.action}
                  className={cn(
                    "shrink-0 font-medium text-[var(--text-primary)]",
                    e.severity === "warn" && "text-[var(--warning)]",
                    e.severity === "error" && "text-[var(--error)]",
                  )}
                >
                  {auditActionLabel(e.action)}
                </span>
                <span className="flex min-w-0 items-center gap-1 text-[var(--text-tertiary)]">
                  by <UserBadge id={e.user} />
                </span>
                <span className="ml-auto shrink-0 text-[var(--text-tertiary)]" title={e.timestamp}>
                  {relativeTime(e.timestamp)}
                </span>
              </div>
              {e.message && <AuditMessage wrap action={e.action} message={e.message} className="text-[var(--text-secondary)]" />}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
