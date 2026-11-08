"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Search } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { useCan, Perms } from "@/features/auth";
import { useUserDirectory } from "@/features/users";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsNotice } from "@/components/settings/SettingsSection";
import { UserBadge } from "@/components/activity/UserBadge";
import { EmptyState } from "@/components/shared/EmptyState";
import { cn } from "@/lib/utils";
import { collapseRuns } from "@/lib/collapse-runs";

// Reads (secret reveals, downloads) are recorded for the trail, not
// because something went wrong: they render neutral whatever the
// server-side severity, so warning colour stays meaningful.
function actionTone(action: string, severity: string): string {
  if (/(_read|\.read|download)$/.test(action)) return "text-[var(--text-primary)]";
  if (severity === "error") return "text-[var(--error)]";
  if (severity === "warn" || severity === "warning") return "text-[var(--warning)]";
  return "text-[var(--text-primary)]";
}

// Audit-log UI. Reads /api/audit (instance-wide; admin-only) or
// /api/audit?project=<n> (project-scoped; Viewer+).
//
// This page calls the cross-project endpoint because /settings/* is
// admin gear; per-project audit lives on the project page itself
// (future work). On a non-admin who somehow lands here the API
// returns 403 and we render an empty state pointing them at the
// project page.
interface AuditRow {
  id: number;
  timestamp: string;
  user: string;
  severity: string;
  action: string;
  namespace: string;
  phase: string;
  app: string;
  pipeline: string;
  resource: string;
  message: string;
}

interface AuditResp {
  audit: AuditRow[];
  count: number;
  limit: number;
}

export default function ActivityPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const [project, setProject] = useState("");
  const [user, setUser] = useState("");
  const [action, setAction] = useState("");

  const query = useQuery<AuditResp>({
    queryKey: ["admin", "audit", { project }],
    // Cross-project view requires admin; the project-scoped path is
    // Viewer-gated and only needs ?project= set. We add a 250-row
    // limit ceiling so a quick filter doesn't pull the whole table.
    queryFn: () => api<AuditResp>(`/api/audit?limit=250${project ? `&project=${encodeURIComponent(project)}` : ""}`),
    staleTime: 15_000,
    retry: false,
  });

  const audit = query.data?.audit;
  const { resolve } = useUserDirectory();

  // Client-side filters layer on top of server-side project filter.
  // Server doesn't index on user/action, so filtering across 250
  // rows in JS is cheaper than three round-trips.
  const filtered = useMemo(() => {
    const u = user.trim().toLowerCase();
    const a = action.trim().toLowerCase();
    return (audit ?? []).filter((r) => {
      if (u) {
        const name = resolve(r.user)?.username.toLowerCase() ?? "";
        if (!r.user.toLowerCase().includes(u) && !name.includes(u)) return false;
      }
      if (a && !r.action.toLowerCase().includes(a)) return false;
      return true;
    });
  }, [audit, user, action, resolve]);

  return (
    <SettingsPage
      title="Activity"
      description={
        <>
          Audit log: who did what, when, against which project. Newest first.
          {!isAdmin && " Set the project filter to see entries you have access to."}
        </>
      }
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <FilterInput label="Project" value={project} onChange={setProject} placeholder="All projects" />
        <FilterInput label="User" value={user} onChange={setUser} placeholder="Username or id" />
        <FilterInput label="Action" value={action} onChange={setAction} placeholder="e.g. delete" />
      </div>

      {query.isPending ? (
        <div className="space-y-2">
          {[...Array(8)].map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : query.isError ? (
        query.error instanceof ApiError && query.error.status === 403 ? (
          <SettingsNotice tone="info" title="Pick a project">
            Instance-wide audit needs admin. Set the project filter to see entries for projects you can access.
          </SettingsNotice>
        ) : (
          <SettingsNotice tone="error" title="Couldn't load the audit log">
            {(query.error as Error).message}
          </SettingsNotice>
        )
      ) : filtered.length === 0 ? (
        <EmptyState title="No matching entries" description="Nothing in the audit log matches these filters." className="p-8" />
      ) : (
        <div role="table" aria-label="Audit log" className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
          <div
            role="row"
            className={cn("hidden border-b border-[var(--border-subtle)] px-4 py-2 text-xs font-medium text-[var(--text-secondary)] md:grid", AUDIT_GRID)}
          >
            <span role="columnheader">When</span>
            <span role="columnheader">Who</span>
            <span role="columnheader">Action</span>
            <span role="columnheader">Target</span>
            <span role="columnheader">Message</span>
          </div>
          <div role="rowgroup" className="divide-y divide-[var(--border-subtle)]">
            {collapseRuns(filtered, (r) => [r.user, r.action, r.pipeline, r.app, r.message, r.severity].join("\u0000")).map(({ item: r, count, last }) => {
              const target = [r.pipeline, r.app].filter(Boolean).join(" / ");
              return (
                <div
                  key={r.id}
                  role="row"
                  className={cn("grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 px-4 py-2.5 text-sm hover:bg-[var(--bg-tertiary)]/40", AUDIT_GRID)}
                >
                  <span
                    role="cell"
                    className="col-start-2 row-start-1 whitespace-nowrap text-xs text-[var(--text-secondary)] md:col-start-auto md:row-start-auto md:text-sm"
                    title={
                      r.timestamp
                        ? count > 1 && last.timestamp
                          ? `${count} times, ${new Date(last.timestamp).toLocaleString()} to ${new Date(r.timestamp).toLocaleString()}`
                          : new Date(r.timestamp).toLocaleString()
                        : undefined
                    }
                  >
                    {r.timestamp ? relativeTime(r.timestamp) : "—"}
                  </span>
                  <span role="cell" className="col-start-1 row-start-1 min-w-0 md:col-start-auto md:row-start-auto">
                    <UserBadge id={r.user} />
                  </span>
                  <span role="cell" className="col-span-2 min-w-0 md:col-span-1">
                    <code className={cn("break-all font-mono text-xs", actionTone(r.action, r.severity))}>{r.action}</code>
                    {count > 1 && (
                      <span
                        className="ml-1.5 inline-flex rounded-full border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-1.5 text-2xs font-medium tabular-nums text-[var(--text-secondary)]"
                        aria-label={`repeated ${count} times`}
                      >
                        ×{count}
                      </span>
                    )}
                  </span>
                  <span role="cell" className="col-span-2 min-w-0 truncate text-xs text-[var(--text-secondary)] md:col-span-1 md:text-sm" title={r.resource ? `${target} (${r.resource})` : target}>
                    {target ? <span className="font-mono text-xs">{target}</span> : <span className="text-[var(--text-tertiary)]">—</span>}
                  </span>
                  <span role="cell" className="col-span-2 min-w-0 truncate text-xs text-[var(--text-secondary)] md:col-span-1 md:text-sm" title={r.message}>
                    {r.message || "—"}
                  </span>
                </div>
              );
            })}
          </div>
          {query.data && query.data.count > filtered.length && (
            <p className="border-t border-[var(--border-subtle)] px-4 py-2 text-xs text-[var(--text-secondary)]">
              Showing {filtered.length} of {query.data.count} entries (identical repeats in a row are folded into one). User and action filters search the latest{" "}
              {audit?.length ?? 0} only.
            </p>
          )}
        </div>
      )}
    </SettingsPage>
  );
}

const AUDIT_GRID =
  "md:grid-cols-[84px_minmax(0,140px)_minmax(0,180px)_minmax(0,160px)_minmax(0,1fr)] md:items-center md:gap-4";

function FilterInput({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-xs font-medium text-[var(--text-secondary)]">{label}</span>
      <span className="relative block">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--text-tertiary)]" aria-hidden />
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className="pl-8"
        />
      </span>
    </label>
  );
}
