"use client";

import { AlertCircle, ShieldAlert, TrendingUp } from "lucide-react";
import { statusChipClass } from "@/components/ui/status-chip";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { errorCountLabel } from "@/features/errors";
import { compactCount, errorBadgeLabel, vulnBadgeLabel, type ErrorSummary, type VulnSummary } from "./badgeSummary";

const badgeBase = "nodrag nopan";

// Security finding marker: shield + count, deliberately quieter than the
// error chip (no fill, no border, muted text) so a card's runtime errors
// always out-rank its image CVEs. Clicking opens the Vulnerabilities tab.
const vulnClass =
  "inline-flex h-[18px] shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-1 text-2xs font-medium tabular-nums leading-none text-[var(--text-tertiary)] transition-colors outline-none hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-secondary)] focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--focus)] [&>svg]:size-3 [&>svg]:shrink-0";
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
            className={cn(badgeBase, vulnClass)}
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
            className={cn(
              badgeBase,
              statusChipClass(hot ? "danger" : "neutral", { interactive: true, size: "sm" }),
              !hot && "text-[var(--text-primary)]",
            )}
          />
        }
      >
        {/* A steady count stays a neutral chip, but the icon keeps the
            error hue so it still out-weighs the CVE marker beside it. */}
        <AlertCircle className={cn(!hot && "text-[var(--error)]")} aria-hidden />
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
