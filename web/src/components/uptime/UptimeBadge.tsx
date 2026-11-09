"use client";

import { uptimeView, type UptimeServiceStatus } from "@/features/uptime";
import { formatLatency, relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

const TONE: Record<string, string> = {
  up: "border-[var(--success)]/30 text-[var(--success)]",
  failing: "border-[var(--warning)]/30 bg-[var(--warning-subtle)] text-[var(--warning)]",
  down: "border-[var(--error)]/30 bg-[var(--error-subtle)] text-[var(--error)]",
};

export function uptimeTitle(s: UptimeServiceStatus): string {
  const parts = [`Health check: ${uptimeView(s).label}`];
  if (s.url) parts.push(s.url);
  if (s.lastCheckedAt) parts.push(`checked ${relativeTime(s.lastCheckedAt)}`);
  if (s.statusCode) parts.push(`HTTP ${s.statusCode}`);
  if (s.latencyMs) parts.push(formatLatency(s.latencyMs));
  if (s.since) parts.push(`failing since ${relativeTime(s.since)}`);
  if (s.reason) parts.push(s.reason);
  if (s.error) parts.push(s.error);
  return parts.join(" · ");
}

export function UptimeBadge({ status }: { status: UptimeServiceStatus }) {
  const v = uptimeView(status);
  return (
    <span
      title={uptimeTitle(status)}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-xs font-medium",
        TONE[status.state] ?? "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
        status.stale && "opacity-60",
      )}
    >
      <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", v.state === "up" ? "bg-[var(--success)]" : v.state === "down" ? "bg-[var(--error)]" : v.state === "failing" ? "bg-[var(--warning)]" : "bg-[var(--text-tertiary)]")} />
      Health check {v.label}
      {v.stale && <span className="font-normal text-[var(--text-tertiary)]">· last known</span>}
    </span>
  );
}
