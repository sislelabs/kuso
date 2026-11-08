"use client";

import { AlertCircle, ShieldAlert } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { compactCount, errorBadgeLabel, vulnBadgeLabel, type ErrorSummary, type VulnSummary } from "./badgeSummary";

const badgeBase =
  "nodrag nopan inline-flex h-5 shrink-0 items-center gap-1 rounded-md border px-1.5 text-[11px] font-medium tabular-nums transition-colors focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--accent)]";

// Security finding marker: shield + count in a neutral/warning style so
// it never reads as a runtime error. Clicking opens the Vulnerabilities tab.
export function VulnBadge({ summary, onOpen }: { summary?: VulnSummary; onOpen?: () => void }) {
  if (!summary || summary.fixableCritical <= 0) return null;
  const label = vulnBadgeLabel(summary);
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={label}
            onClick={(e) => {
              e.stopPropagation();
              onOpen?.();
            }}
            className={cn(
              badgeBase,
              "border-[var(--border-subtle)] bg-[var(--bg-tertiary)] text-[var(--text-secondary)] hover:border-[var(--warning)]/60 hover:text-[var(--text-primary)]",
            )}
          />
        }
      >
        <ShieldAlert className="h-3 w-3 text-[var(--warning)]" aria-hidden />
        {compactCount(summary.fixableCritical)}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

// Runtime errors seen in the logs over 24h, labelled in words so it
// can't be confused with the vuln count.
export function ErrorBadge({ summary, onOpen }: { summary?: ErrorSummary; onOpen?: () => void }) {
  if (!summary || summary.occurrences <= 0) return null;
  const label = errorBadgeLabel(summary);
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={label}
            onClick={(e) => {
              e.stopPropagation();
              onOpen?.();
            }}
            className={cn(
              badgeBase,
              "border-[var(--error)]/35 bg-[var(--error-subtle)] text-[var(--error)] hover:border-[var(--error)]/70",
            )}
          />
        }
      >
        <AlertCircle className="h-3 w-3" aria-hidden />
        {compactCount(summary.occurrences)} {summary.occurrences === 1 ? "error" : "errors"}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
