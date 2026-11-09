"use client";

import { AlertCircle, ShieldAlert, TrendingUp } from "lucide-react";
import { statusChipClass } from "@/components/ui/status-chip";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { errorCountLabel } from "@/features/errors";
import { compactCount, errorBadgeLabel, vulnBadgeLabel, type ErrorSummary, type VulnSummary } from "./badgeSummary";

const badgeBase = "nodrag nopan";

// Security finding marker: shield + count in a neutral/warning style so
// it never reads as a runtime error. Clicking opens the Vulnerabilities tab.
export function VulnBadge({
  summary,
  onOpen,
  tabIndex,
}: {
  summary?: VulnSummary;
  onOpen?: () => void;
  tabIndex?: number;
}) {
  if (!summary || summary.fixableCritical <= 0) return null;
  const label = vulnBadgeLabel(summary);
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            tabIndex={tabIndex}
            aria-label={label}
            onClick={(e) => {
              e.stopPropagation();
              onOpen?.();
            }}
            className={cn(badgeBase, statusChipClass("neutral", { interactive: true, size: "sm" }))}
          />
        }
      >
        <ShieldAlert className="text-[var(--warning)]" aria-hidden />
        {compactCount(summary.fixableCritical)} {summary.fixableCritical === 1 ? "CVE" : "CVEs"}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

// Runtime errors seen in the logs over 24h, labelled in words so it
// can't be confused with the vuln count. Red only when `hot` (new or
// spiking, see errorChipHot); a steady count stays neutral.
export function ErrorBadge({
  summary,
  onOpen,
  tabIndex,
  compact = false,
  hot = true,
}: {
  summary?: ErrorSummary;
  onOpen?: () => void;
  tabIndex?: number;
  hot?: boolean;
  // Canvas cards drop the "· 24h" (it's in the tooltip) so the name keeps room.
  compact?: boolean;
}) {
  if (!summary || summary.occurrences <= 0) return null;
  const label = `${errorBadgeLabel(summary)}${hot ? "" : " Steady, not spiking."}`;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            tabIndex={tabIndex}
            aria-label={label}
            onClick={(e) => {
              e.stopPropagation();
              onOpen?.();
            }}
            className={cn(badgeBase, statusChipClass(hot ? "danger" : "neutral", { interactive: true, size: "sm" }))}
          />
        }
      >
        <AlertCircle className={cn(!hot && "text-[var(--text-tertiary)]")} aria-hidden />
        {compact
          ? `${compactCount(summary.occurrences)} ${summary.occurrences === 1 ? "error" : "errors"}`
          : errorCountLabel(summary.occurrences)}
        {/* New or spiking reads from the arrow too, not only the red. */}
        {hot && <TrendingUp aria-hidden />}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
