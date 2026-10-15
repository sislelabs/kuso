"use client";

import type { UptimeServiceStatus } from "@/features/uptime";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

const TONE: Record<string, string> = {
  up: "border-[var(--success)]/30 text-[var(--success)]",
  failing: "border-[var(--warning)]/30 bg-[var(--warning-subtle)] text-[var(--warning)]",
  down: "border-[var(--error)]/30 bg-[var(--error-subtle)] text-[var(--error)]",
};

export function uptimeTitle(s: UptimeServiceStatus): string {
  const parts = [`uptime: ${s.state}${s.stale ? " (stale)" : ""}`];
  if (s.url) parts.push(s.url);
  if (s.lastCheckedAt) parts.push(`checked ${relativeTime(s.lastCheckedAt)}`);
  if (s.statusCode) parts.push(`HTTP ${s.statusCode}`);
  if (s.latencyMs) parts.push(`${s.latencyMs}ms`);
  if (s.since) parts.push(`failing since ${relativeTime(s.since)}`);
  if (s.reason) parts.push(s.reason);
  if (s.error) parts.push(s.error);
  return parts.join(" · ");
}

export function UptimeBadge({ status }: { status: UptimeServiceStatus }) {
  return (
    <span
      title={uptimeTitle(status)}
      className={cn(
        "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 font-mono text-[10px]",
        TONE[status.state] ?? "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
        status.stale && "opacity-60",
      )}
    >
      uptime {status.state}
      {status.state === "up" && status.latencyMs ? ` · ${status.latencyMs}ms` : ""}
    </span>
  );
}
