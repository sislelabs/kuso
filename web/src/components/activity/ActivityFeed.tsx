"use client";

import { useAudit, type AuditEntry } from "@/features/activity";
import { relativeTime } from "@/lib/format";
import { Activity, Clock, AlertTriangle, Info, CheckCircle } from "lucide-react";
import { cn } from "@/lib/utils";

function severityIcon(s?: string) {
  const x = (s ?? "").toLowerCase();
  if (x === "error" || x === "fatal") return AlertTriangle;
  if (x === "warning" || x === "warn") return AlertTriangle;
  if (x === "ok" || x === "success") return CheckCircle;
  return Info;
}

function severityColor(s?: string) {
  const x = (s ?? "").toLowerCase();
  if (x === "error" || x === "fatal") return "text-red-500";
  if (x === "warning" || x === "warn") return "text-amber-500";
  if (x === "ok" || x === "success") return "text-emerald-500";
  return "text-[var(--text-tertiary)]";
}

function groupByDay(entries: AuditEntry[]): Record<string, AuditEntry[]> {
  const out: Record<string, AuditEntry[]> = {};
  for (const e of entries) {
    const t = e.timestamp ? new Date(e.timestamp) : new Date();
    const key = t.toISOString().slice(0, 10);
    out[key] ??= [];
    out[key].push(e);
  }
  return out;
}

export function ActivityFeed({ filter }: { filter?: (e: AuditEntry) => boolean }) {
  const { data, isPending, isError, error } = useAudit(200);

  if (isPending) {
    return <p className="text-sm text-[var(--text-tertiary)]">loading…</p>;
  }
  if (isError) {
    return (
      <p className="text-sm text-red-500">
        Failed to load activity: {error?.message}
      </p>
    );
  }
  let entries = data?.audit ?? [];
  if (filter) entries = entries.filter(filter);
  if (entries.length === 0) {
    return (
      <p className="text-sm text-[var(--text-tertiary)]">No activity yet.</p>
    );
  }

  const groups = groupByDay(entries);
  const keys = Object.keys(groups).sort().reverse();

  return (
    <div className="space-y-6">
      {keys.map((day) => (
        <div key={day}>
          <h3 className="mb-2 flex items-center gap-2 text-[10px] font-mono uppercase tracking-widest text-[var(--text-tertiary)]">
            <Clock className="h-3 w-3" />
            {day}
          </h3>
          <ul className="space-y-1">
            {groups[day].map((e) => {
              const Icon = severityIcon(e.severity);
              const colorClass = severityColor(e.severity);
              return (
                <li
                  key={e.id}
                  className="group flex items-start gap-3 rounded-md px-2 py-2 hover:bg-[var(--bg-tertiary)]"
                >
                  <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", colorClass)} />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm text-[var(--text-primary)]">
                      {e.action ?? "event"}
                      {e.message ? <span className="text-[var(--text-secondary)]">: {e.message}</span> : null}
                    </p>
                    <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                      {[e.pipelineName, e.phaseName, e.appName]
                        .filter(Boolean)
                        .join(" / ")}
                      {e.user ? ` · ${e.user}` : ""}
                    </p>
                  </div>
                  <span className="font-mono text-[10px] text-[var(--text-tertiary)] whitespace-nowrap">
                    {relativeTime(e.timestamp)}
                  </span>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </div>
  );
}

export function ActivityHeader() {
  return (
    <div className="flex items-center gap-2">
      <Activity className="h-4 w-4 text-[var(--text-secondary)]" />
      <span className="font-heading text-base font-semibold">Activity</span>
    </div>
  );
}
