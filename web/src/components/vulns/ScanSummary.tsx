import type { ReactNode } from "react";
import { AlertTriangle, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { relativeTime } from "@/lib/format";
import { isScanActive, type Severity, type VulnImageReport, type VulnScanSummary } from "@/features/vulns";
import { SEVERITY_TONE } from "./SeverityBadge";

const STATUS_TONE: Record<VulnScanSummary["status"], string> = {
  queued: "text-[var(--building)] bg-[var(--building-subtle)]",
  running: "text-[var(--building)] bg-[var(--building-subtle)]",
  succeeded: "text-[var(--success)] bg-[var(--success-subtle)]",
  failed: "text-[var(--error)] bg-[var(--error-subtle)]",
};

function CountTile({ severity, count, fixable }: { severity: Severity; count: number; fixable?: number }) {
  const tone = SEVERITY_TONE[severity];
  return (
    <div className={cn("min-w-[72px] rounded-md border px-2.5 py-1.5", tone.border, count > 0 ? tone.bg : "")}>
      <div className={cn("text-xs font-semibold", tone.text)}>{severity.charAt(0) + severity.slice(1).toLowerCase()}</div>
      <div className="flex items-baseline gap-1.5">
        <span className="font-mono text-lg font-semibold text-[var(--text-primary)]">{count}</span>
        {fixable !== undefined && (
          <span className="text-xs text-[var(--text-tertiary)]" title="Fixable by upgrading the package">
            {fixable} fixable
          </span>
        )}
      </div>
    </div>
  );
}

function shortDigest(d: string): string {
  const hex = d.startsWith("sha256:") ? d.slice(7) : d;
  return `sha256:${hex.slice(0, 12)}`;
}

export function ScanSummary({ image, actions }: { image: VulnImageReport; actions?: ReactNode }) {
  const scan = image.scan;
  return (
    <section className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <p className="truncate font-mono text-xs text-[var(--text-primary)]" title={image.imageRef}>
            {image.imageRef}
          </p>
          <p className="font-mono text-xs text-[var(--text-tertiary)]" title={image.digest}>
            {shortDigest(image.digest)}
          </p>
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>

      {scan ? (
        <>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
            <span
              className={cn(
                "inline-flex items-center gap-1 rounded px-1.5 py-px font-mono text-xs",
                STATUS_TONE[scan.status],
              )}
            >
              {isScanActive(scan) && <Loader2 className="h-3 w-3 animate-spin" />}
              {scan.status}
            </span>
            <span title={scan.finishedAt ?? scan.createdAt}>
              {scan.finishedAt
                ? `scanned ${relativeTime(scan.finishedAt)}`
                : `requested ${relativeTime(scan.createdAt)}`}
            </span>
            <span>({scan.reason})</span>
            {scan.dbUpdatedAt && (
              <span title={scan.dbUpdatedAt}>vuln DB {new Date(scan.dbUpdatedAt).toLocaleDateString()}</span>
            )}
            {scan.ignoredCount > 0 && <span>{scan.ignoredCount} ignored (not counted)</span>}
          </div>

          {scan.status === "failed" && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-3 text-xs"
            >
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--error)]" />
              <div className="min-w-0">
                <p className="font-medium text-[var(--text-primary)]">Scan failed. This is not a clean result.</p>
                {scan.error && (
                  <p className="mt-0.5 break-words font-mono text-[var(--text-secondary)]">{scan.error}</p>
                )}
              </div>
            </div>
          )}

          {scan.status === "succeeded" && <Verdict counts={scan.counts} />}

          {scan.status === "succeeded" && (
            <div className="flex flex-wrap gap-2">
              <CountTile severity="CRITICAL" count={scan.counts.critical} fixable={scan.counts.fixableCritical} />
              <CountTile severity="HIGH" count={scan.counts.high} fixable={scan.counts.fixableHigh} />
              <CountTile severity="MEDIUM" count={scan.counts.medium} />
              <CountTile severity="LOW" count={scan.counts.low} />
              <CountTile severity="UNKNOWN" count={scan.counts.unknown} />
            </div>
          )}
        </>
      ) : (
        <p className="text-xs text-[var(--text-secondary)]">
          Not scanned yet. Scans run after each build and nightly; rescan to check now.
        </p>
      )}
    </section>
  );
}

// Verdict is the one-sentence answer above the tiles: is there anything
// serious, and can upgrading fix it.
function Verdict({ counts }: { counts: VulnScanSummary["counts"] }) {
  const serious = counts.critical + counts.high;
  const fixable = counts.fixableCritical + counts.fixableHigh;
  if (serious === 0) {
    return (
      <p className="text-sm text-[var(--text-primary)]">
        No critical or high vulnerabilities.
        {counts.medium + counts.low > 0 && (
          <span className="text-[var(--text-secondary)]"> {counts.medium + counts.low} medium/low remain.</span>
        )}
      </p>
    );
  }
  const parts = [
    counts.critical ? `${counts.critical} critical` : "",
    counts.high ? `${counts.high} high` : "",
  ].filter(Boolean);
  return (
    <p className={cn("text-sm font-medium", counts.critical ? "text-[var(--error)]" : "text-[var(--warning)]")}>
      {parts.join(" and ")} {serious === 1 ? "vulnerability" : "vulnerabilities"}
      <span className="font-normal text-[var(--text-secondary)]">
        {fixable > 0
          ? ` · ${fixable} fixable by upgrading the affected packages and rebuilding.`
          : " · no fixed versions published yet."}
      </span>
    </p>
  );
}
