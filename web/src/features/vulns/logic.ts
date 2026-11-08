import { SEVERITIES, type Severity, type VulnFinding, type VulnScanSummary } from "./types";

export function severityRank(s: Severity): number {
  const i = SEVERITIES.indexOf(s);
  return i === -1 ? SEVERITIES.length : i;
}

export function isFixable(f: Pick<VulnFinding, "fixedVersion">): boolean {
  return !!f.fixedVersion?.trim();
}

// Most severe first; within a severity, fixable before unfixable (those
// are the ones a rebuild can actually clear), then by ID for stability.
export function sortFindings(findings: readonly VulnFinding[]): VulnFinding[] {
  return [...findings].sort(
    (a, b) =>
      severityRank(a.severity) - severityRank(b.severity) ||
      Number(isFixable(b)) - Number(isFixable(a)) ||
      a.vulnId.localeCompare(b.vulnId) ||
      a.pkgName.localeCompare(b.pkgName),
  );
}

export type FindingFilter = {
  // Empty = every severity.
  severities: ReadonlySet<Severity>;
  fixableOnly: boolean;
  showIgnored: boolean;
};

export function filterFindings(findings: readonly VulnFinding[], f: FindingFilter): VulnFinding[] {
  return findings.filter(
    (x) =>
      (f.severities.size === 0 || f.severities.has(x.severity)) &&
      (!f.fixableOnly || isFixable(x)) &&
      (f.showIgnored || !x.ignored),
  );
}

export function countBySeverity(findings: readonly VulnFinding[]): Record<Severity, number> {
  const out: Record<Severity, number> = { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0, UNKNOWN: 0 };
  for (const f of findings) out[f.severity] = (out[f.severity] ?? 0) + 1;
  return out;
}

export const MAX_IGNORE_DAYS = 365;

export type ExpiryParse = { ok: true; days: number | undefined } | { ok: false; error: string };

// Blank = never expires. Otherwise a whole number of days in 1..365,
// matching the server's expiresInDays bound.
export function parseExpiryDays(raw: string): ExpiryParse {
  const v = raw.trim();
  if (v === "") return { ok: true, days: undefined };
  if (!/^\d+$/.test(v)) return { ok: false, error: "Expiry must be a whole number of days." };
  const days = Number(v);
  if (days < 1 || days > MAX_IGNORE_DAYS) {
    return { ok: false, error: `Expiry must be between 1 and ${MAX_IGNORE_DAYS} days.` };
  }
  return { ok: true, days };
}

export function isScanActive(scan: Pick<VulnScanSummary, "status"> | null | undefined): boolean {
  return scan?.status === "queued" || scan?.status === "running";
}

// After a rescan the GET can briefly still return the previous (terminal)
// scan; keep polling until the requested scan id shows up and settles.
export function shouldPollScan(
  scan: Pick<VulnScanSummary, "id" | "status"> | null | undefined,
  awaitingScanId: number | null,
): boolean {
  if (isScanActive(scan)) return true;
  if (awaitingScanId === null) return false;
  return !scan || scan.id < awaitingScanId;
}

// Only http(s) URLs from scanner output are rendered as links; anything
// else (javascript:, data:, relative) is shown as plain text.
export function safeHttpUrl(raw: string | undefined): string | undefined {
  if (!raw) return undefined;
  try {
    const u = new URL(raw);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}
