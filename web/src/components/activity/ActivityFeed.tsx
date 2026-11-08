"use client";

import { useAudit, type AuditEntry } from "@/features/activity";
import { relativeTime } from "@/lib/format";
import { Activity, Clock, AlertTriangle, Info, CheckCircle } from "lucide-react";
import { cn } from "@/lib/utils";
import { LoadingState } from "@/components/ui/loading-state";
import { ExpandableText } from "./ExpandableText";
import { UserBadge } from "./UserBadge";

function severityIcon(s?: string) {
  const x = (s ?? "").toLowerCase();
  if (x === "error" || x === "fatal") return AlertTriangle;
  if (x === "warning" || x === "warn") return AlertTriangle;
  if (x === "ok" || x === "success") return CheckCircle;
  return Info;
}

function severityColor(s?: string) {
  const x = (s ?? "").toLowerCase();
  if (x === "error" || x === "fatal") return "text-[var(--error)]";
  if (x === "warning" || x === "warn") return "text-[var(--warning)]";
  if (x === "ok" || x === "success") return "text-[var(--success)]";
  return "text-[var(--text-tertiary)]";
}

// Day buckets follow the viewer's calendar, not UTC: toISOString() would
// file a 01:00 local event (UTC+3) under the previous day.
export function localDayKey(t: Date): string {
  const m = String(t.getMonth() + 1).padStart(2, "0");
  const d = String(t.getDate()).padStart(2, "0");
  return `${t.getFullYear()}-${m}-${d}`;
}

function groupByDay(entries: AuditEntry[]): Record<string, AuditEntry[]> {
  const out: Record<string, AuditEntry[]> = {};
  for (const e of entries) {
    const t = e.timestamp ? new Date(e.timestamp) : new Date();
    const key = localDayKey(t);
    out[key] ??= [];
    out[key].push(e);
  }
  return out;
}

export function ActivityFeed({ filter }: { filter?: (e: AuditEntry) => boolean }) {
  const { data, isPending, isError, error } = useAudit(200);

  if (isPending) {
    return <LoadingState kind="list" />;
  }
  if (isError) {
    return (
      <p className="text-sm text-[var(--error)]">
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
          <h3 className="mb-2 flex items-center gap-2 text-xs font-medium text-[var(--text-secondary)]">
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
                    <div className="flex min-w-0 items-baseline gap-1 text-sm text-[var(--text-primary)]">
                      <span className="shrink-0">
                        {e.action ?? "event"}
                        {e.message ? ":" : null}
                      </span>
                      {e.message ? (
                        <ExpandableText text={e.message} className="flex-1 text-[var(--text-secondary)]" />
                      ) : null}
                    </div>
                    <p className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-xs text-[var(--text-secondary)]">
                      <UserBadge id={e.user ?? ""} />
                      <span className="font-mono">
                        {[e.pipelineName, e.phaseName, e.appName].filter(Boolean).join(" / ")}
                      </span>
                    </p>
                  </div>
                  <span className="whitespace-nowrap text-xs text-[var(--text-secondary)]">
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
