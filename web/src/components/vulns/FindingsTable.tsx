"use client";

import { useMemo, useState } from "react";
import { ArrowRight, EyeOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";
import {
  SEVERITIES,
  countBySeverity,
  filterFindings,
  isFixable,
  safeHttpUrl,
  sortFindings,
  type Severity,
  type VulnFinding,
} from "@/features/vulns";
import { SEVERITY_TONE, SeverityBadge } from "./SeverityBadge";

function VulnIdCell({ f }: { f: VulnFinding }) {
  const href = safeHttpUrl(f.primaryURL);
  if (!href) return <span className="font-mono text-xs">{f.vulnId}</span>;
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="font-mono text-xs text-[var(--accent)] underline-offset-2 hover:underline"
    >
      {f.vulnId}
    </a>
  );
}

function FilterChip({
  active,
  onClick,
  children,
  className,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-xs transition-colors",
        active
          ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
          : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:border-[var(--border-strong)]",
        className,
      )}
    >
      {children}
    </button>
  );
}

export function FindingsTable({
  findings,
  onIgnore,
}: {
  findings: readonly VulnFinding[];
  // Omitted for read-only viewers and for platform images.
  onIgnore?: (f: VulnFinding) => void;
}) {
  const [severities, setSeverities] = useState<ReadonlySet<Severity>>(new Set());
  const [fixableOnly, setFixableOnly] = useState(false);
  const [showIgnored, setShowIgnored] = useState(true);

  const sorted = useMemo(() => sortFindings(findings), [findings]);
  const visible = useMemo(
    () => filterFindings(sorted, { severities, fixableOnly, showIgnored }),
    [sorted, severities, fixableOnly, showIgnored],
  );
  const counts = useMemo(
    () => countBySeverity(filterFindings(findings, { severities: new Set(), fixableOnly, showIgnored })),
    [findings, fixableOnly, showIgnored],
  );

  const toggleSeverity = (s: Severity) =>
    setSeverities((prev) => {
      const next = new Set(prev);
      if (next.has(s)) next.delete(s);
      else next.add(s);
      return next;
    });

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-1.5">
        {SEVERITIES.map((s) => (
          <FilterChip
            key={s}
            active={severities.has(s)}
            onClick={() => toggleSeverity(s)}
            className={severities.has(s) ? SEVERITY_TONE[s].text : undefined}
          >
            {s.toLowerCase()} <span className="text-[var(--text-tertiary)]">{counts[s]}</span>
          </FilterChip>
        ))}
        <span className="mx-1 h-4 w-px bg-[var(--border-subtle)]" />
        <FilterChip active={fixableOnly} onClick={() => setFixableOnly((v) => !v)}>
          fixable only
        </FilterChip>
        <FilterChip active={showIgnored} onClick={() => setShowIgnored((v) => !v)}>
          show ignored
        </FilterChip>
      </div>

      {findings.length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-secondary)]">
          No known vulnerabilities in this image.
        </p>
      ) : visible.length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-secondary)]">
          No findings match these filters.
        </p>
      ) : (
        <div className="rounded-md border border-[var(--border-subtle)]">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-24">Severity</TableHead>
                <TableHead>Vulnerability</TableHead>
                <TableHead>Package</TableHead>
                <TableHead>Installed → fixed</TableHead>
                <TableHead>Title</TableHead>
                {onIgnore && <TableHead className="w-20" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {visible.map((f) => (
                <TableRow
                  key={`${f.vulnId}|${f.pkgName}|${f.installedVersion}|${f.target ?? ""}`}
                  className={cn(f.ignored && "opacity-60")}
                >
                  <TableCell>
                    <SeverityBadge severity={f.severity} />
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <VulnIdCell f={f} />
                      {f.ignored && (
                        <span
                          className="inline-flex items-center gap-0.5 text-xs text-[var(--text-tertiary)]"
                          title="Ignored: excluded from the deploy gate and alerts"
                        >
                          <EyeOff className="h-3 w-3" /> ignored
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <span className="font-mono text-xs" title={f.target}>
                      {f.pkgName}
                    </span>
                    <span className="ml-1 text-xs text-[var(--text-tertiary)]">{f.pkgType}</span>
                  </TableCell>
                  <TableCell>
                    <span className="inline-flex items-center gap-1 font-mono text-xs">
                      {f.installedVersion}
                      <ArrowRight className="h-3 w-3 text-[var(--text-tertiary)]" />
                      {isFixable(f) ? (
                        <span className="text-[var(--success)]">{f.fixedVersion}</span>
                      ) : (
                        <span className="text-[var(--text-tertiary)]">no fix</span>
                      )}
                    </span>
                  </TableCell>
                  <TableCell className="max-w-[320px]">
                    <span className="line-clamp-2 text-xs text-[var(--text-secondary)]" title={f.title}>
                      {f.title ?? ""}
                    </span>
                  </TableCell>
                  {onIgnore && (
                    <TableCell className="text-right">
                      {!f.ignored && (
                        <Button variant="ghost" size="xs" onClick={() => onIgnore(f)}>
                          Ignore…
                        </Button>
                      )}
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}
