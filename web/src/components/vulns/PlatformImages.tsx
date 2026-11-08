"use client";

import { Fragment, useState } from "react";
import { ChevronDown, ChevronRight, Loader2 } from "lucide-react";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { isScanActive, usePlatformFindings, usePlatformVulns, type VulnImageReport } from "@/features/vulns";
import { FindingsTable } from "./FindingsTable";
import { SEVERITY_TONE } from "./SeverityBadge";

function rowKey(i: VulnImageReport): string {
  return `${i.digest}|${i.namespace ?? ""}|${i.workload ?? ""}`;
}

function PlatformFindings({ digest }: { digest: string }) {
  const q = usePlatformFindings(digest);
  if (q.isError) return <QueryErrorState what="findings" error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Skeleton className="h-24 w-full" />;
  return <FindingsTable findings={q.data.findings} />;
}

function Count({ n, tone }: { n: number | undefined; tone: string }) {
  if (n === undefined) return <span className="text-[var(--text-tertiary)]">–</span>;
  return <span className={cn("font-mono text-xs", n > 0 ? tone : "text-[var(--text-tertiary)]")}>{n}</span>;
}

function StatusCell({ img }: { img: VulnImageReport }) {
  const scan = img.scan;
  if (!scan) return <span className="text-xs text-[var(--text-tertiary)]">Not scanned</span>;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-xs capitalize",
        scan.status === "failed" && "text-[var(--error)]",
        isScanActive(scan) && "text-[var(--building)]",
      )}
      title={scan.error}
    >
      {isScanActive(scan) && <Loader2 className="h-3 w-3 animate-spin" />}
      {scan.status}
    </span>
  );
}

export function PlatformImages() {
  const q = usePlatformVulns();
  const [open, setOpen] = useState<string | null>(null);

  if (q.isError) return <QueryErrorState what="platform images" error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Skeleton className="h-40 w-full" />;
  if (q.data.images.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-secondary)]">
        No platform images recorded yet. They are inventoried by the nightly sweep; &ldquo;Scan everything now&rdquo;
        runs it immediately.
      </p>
    );
  }

  return (
    <div className="rounded-md border border-[var(--border-subtle)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-6" />
            <TableHead>Workload</TableHead>
            <TableHead>Image</TableHead>
            <TableHead>Status</TableHead>
            <TableHead title="Critical (fixable in brackets)">Critical</TableHead>
            <TableHead title="High (fixable in brackets)">High</TableHead>
            <TableHead>Medium</TableHead>
            <TableHead>Low</TableHead>
            <TableHead>Scanned</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {q.data.images.map((img) => {
            const key = rowKey(img);
            const expanded = open === key;
            const c = img.scan?.status === "succeeded" ? img.scan.counts : undefined;
            return (
              <Fragment key={key}>
                <TableRow className="cursor-pointer" onClick={() => setOpen(expanded ? null : key)}>
                  <TableCell>
                    <button
                      type="button"
                      aria-expanded={expanded}
                      aria-label={expanded ? "Hide findings" : "Show findings"}
                      className="inline-flex size-6 items-center justify-center rounded text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                    >
                      {expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                    </button>
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {img.namespace ? `${img.namespace}/` : ""}
                    {img.workload ?? "?"}
                  </TableCell>
                  <TableCell className="max-w-[220px] truncate font-mono text-xs" title={img.imageRef}>
                    {img.imageRef}
                  </TableCell>
                  <TableCell>
                    <StatusCell img={img} />
                  </TableCell>
                  <TableCell>
                    <Count n={c?.critical} tone={SEVERITY_TONE.CRITICAL.text} />
                    {c && <span className="ml-1 text-xs text-[var(--text-tertiary)]">({c.fixableCritical})</span>}
                  </TableCell>
                  <TableCell>
                    <Count n={c?.high} tone={SEVERITY_TONE.HIGH.text} />
                    {c && <span className="ml-1 text-xs text-[var(--text-tertiary)]">({c.fixableHigh})</span>}
                  </TableCell>
                  <TableCell>
                    <Count n={c?.medium} tone={SEVERITY_TONE.MEDIUM.text} />
                  </TableCell>
                  <TableCell>
                    <Count n={c?.low} tone={SEVERITY_TONE.LOW.text} />
                  </TableCell>
                  <TableCell className="text-xs text-[var(--text-secondary)]">
                    {relativeTime(img.scan?.finishedAt)}
                  </TableCell>
                </TableRow>
                {expanded && (
                  <TableRow>
                    <TableCell colSpan={9} className="bg-[var(--bg-primary)] p-4">
                      {img.scan?.status === "failed" && img.scan.error && (
                        <p className="mb-3 break-words font-mono text-xs text-[var(--error)]">{img.scan.error}</p>
                      )}
                      <PlatformFindings digest={img.digest} />
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
