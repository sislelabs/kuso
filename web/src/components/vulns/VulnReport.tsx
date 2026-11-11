"use client";

import { useState, type ReactNode } from "react";
import { RefreshCw, ShieldOff, PackageX } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/shared/EmptyState";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { isScanActive, type VulnFinding, type VulnImageReport, type VulnOwner } from "@/features/vulns";
import { ScanSummary } from "./ScanSummary";
import { FindingsTable } from "./FindingsTable";
import { IgnoreDialog } from "./IgnoreDialog";
import { IgnoreList } from "./IgnoreList";

type ReportQuery = {
  data: { image: VulnImageReport | null; findings: VulnFinding[] } | undefined;
  isError: boolean;
  error: unknown;
  refetch: () => unknown;
};

export function VulnReport({
  owner,
  query,
  canEdit,
  canIgnore = canEdit,
  ignoreHint,
  modeOff,
  modeOffHint,
  header,
  onRescan,
  rescanPending,
}: {
  owner: VulnOwner;
  query: ReportQuery;
  canEdit: boolean;
  // Adding an ignore can need more than other edits (an enforce gate
  // takes a project admin to loosen); ignoreHint says why it's off.
  canIgnore?: boolean;
  ignoreHint?: string;
  modeOff: boolean;
  modeOffHint: string;
  // Rendered above everything (the service policy selector).
  header?: ReactNode;
  onRescan: () => void;
  rescanPending: boolean;
}) {
  const [ignoring, setIgnoring] = useState<VulnFinding | null>(null);

  if (query.isError) {
    return (
      <div className="space-y-4">
        {header}
        <QueryErrorState what="vulnerabilities" error={query.error} onRetry={() => void query.refetch()} />
      </div>
    );
  }
  if (!query.data) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const { image, findings } = query.data;
  const scanBusy = isScanActive(image?.scan);
  const rescanButton =
    canEdit && !modeOff && image ? (
      <Button size="sm" variant="outline" onClick={onRescan} disabled={rescanPending || scanBusy}>
        <RefreshCw className={scanBusy ? "animate-spin" : undefined} />
        {scanBusy ? "Scanning…" : "Rescan"}
      </Button>
    ) : null;

  return (
    <div className="space-y-5">
      {header}
      {modeOff ? (
        <EmptyState icon={<ShieldOff className="h-5 w-5" />} title="Scanning is off" description={modeOffHint} />
      ) : !image ? (
        <EmptyState
          icon={<PackageX className="h-5 w-5" />}
          title="No live image"
          description={`Nothing is running for this ${owner.kind} yet, so there is no image to scan. Findings appear after the first deploy.`}
        />
      ) : (
        <>
          <ScanSummary image={image} actions={rescanButton} />
          {/* While a rescan runs the server may still return the previous
              scan's findings; keep showing them rather than blanking. */}
          {(image.scan?.status === "succeeded" || findings.length > 0) && (
            <FindingsTable findings={findings} onIgnore={canIgnore ? setIgnoring : undefined} />
          )}
          {canEdit && !canIgnore && ignoreHint && (
            <p className="text-xs text-[var(--text-secondary)]">{ignoreHint}</p>
          )}
          <IgnoreList owner={owner} canEdit={canEdit} />
          <IgnoreDialog owner={owner} finding={ignoring} onClose={() => setIgnoring(null)} />
        </>
      )}
    </div>
  );
}
