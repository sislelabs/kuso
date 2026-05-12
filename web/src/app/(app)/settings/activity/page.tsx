"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Activity, Filter } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { useCan, Perms } from "@/features/auth";

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

  const rows = query.data?.audit ?? [];

  // Client-side filters layer on top of server-side project filter.
  // Server doesn't index on user/action, so filtering across 250
  // rows in JS is cheaper than three round-trips.
  const filtered = useMemo(() => {
    const u = user.trim().toLowerCase();
    const a = action.trim().toLowerCase();
    return rows.filter((r) => {
      if (u && !r.user.toLowerCase().includes(u)) return false;
      if (a && !r.action.toLowerCase().includes(a)) return false;
      return true;
    });
  }, [rows, user, action]);

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <header className="mb-6 flex items-start gap-3">
        <Activity className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Activity</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Audit log: who did what, when, against which project. Newest first.
            {!isAdmin && " Set the project filter to see entries you have access to."}
          </p>
        </div>
      </header>

      <div className="mb-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
        <FilterInput
          label="Project"
          value={project}
          onChange={setProject}
          placeholder="all projects"
        />
        <FilterInput
          label="User"
          value={user}
          onChange={setUser}
          placeholder="username or id"
        />
        <FilterInput
          label="Action"
          value={action}
          onChange={setAction}
          placeholder="e.g. create, delete"
        />
      </div>

      {query.isPending ? (
        <div className="space-y-2">
          {[...Array(8)].map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : query.isError ? (
        <div className="rounded-md border border-red-500/40 bg-red-500/5 p-4 text-sm text-red-300">
          {(query.error as Error).message.includes("403")
            ? "Set a project filter to see audit entries — instance-wide audit requires admin."
            : `Failed to load audit log: ${(query.error as Error).message}`}
        </div>
      ) : filtered.length === 0 ? (
        <div className="rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-6 text-center text-sm text-[var(--text-tertiary)]">
          No audit entries match your filters.
        </div>
      ) : (
        <div className="rounded-md border border-[var(--border-subtle)] overflow-hidden">
          <table className="w-full text-[12px]">
            <thead className="bg-[var(--bg-secondary)] text-[var(--text-tertiary)]">
              <tr>
                <Th>When</Th>
                <Th>Who</Th>
                <Th>Action</Th>
                <Th>Project</Th>
                <Th>Resource</Th>
                <Th className="hidden md:table-cell">Message</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--border-subtle)]">
              {filtered.map((r) => (
                <tr key={r.id} className="hover:bg-[var(--bg-secondary)]/40">
                  <Td className="whitespace-nowrap font-mono text-[11px] text-[var(--text-tertiary)]">
                    {r.timestamp ? relativeTime(r.timestamp) : "—"}
                  </Td>
                  <Td className="font-mono text-[11px]">{r.user || "system"}</Td>
                  <Td className="font-mono text-[11px]">
                    <span
                      className={
                        r.severity === "error"
                          ? "text-red-300"
                          : r.severity === "warning"
                            ? "text-amber-300"
                            : "text-[var(--text-primary)]"
                      }
                    >
                      {r.action}
                    </span>
                  </Td>
                  <Td className="font-mono text-[11px] text-[var(--text-secondary)]">
                    {r.pipeline || "—"}
                  </Td>
                  <Td className="font-mono text-[11px] text-[var(--text-secondary)]">
                    {[r.app, r.resource].filter(Boolean).join(" · ") || "—"}
                  </Td>
                  <Td className="hidden md:table-cell max-w-[28ch] truncate text-[var(--text-secondary)]" title={r.message}>
                    {r.message || "—"}
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
          {query.data && query.data.count > filtered.length && (
            <p className="border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 font-mono text-[10px] text-[var(--text-tertiary)]">
              showing {filtered.length} of {query.data.count} entries — narrow filters to see older rows
            </p>
          )}
        </div>
      )}
    </div>
  );
}

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
    <label className="flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2.5">
      <Filter className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
      <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </span>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-8 border-0 bg-transparent px-0 font-mono text-[12px] focus-visible:ring-0"
      />
    </label>
  );
}

function Th({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <th className={`px-3 py-2 text-left font-mono text-[10px] uppercase tracking-widest font-normal ${className ?? ""}`}>
      {children}
    </th>
  );
}

function Td({ children, className, title }: { children: React.ReactNode; className?: string; title?: string }) {
  return (
    <td className={`px-3 py-2 ${className ?? ""}`} title={title}>
      {children}
    </td>
  );
}
