import type { VulnImageReport } from "@/features/vulns/types";
import { serviceShortName } from "@/lib/utils";
import { errorCountLabel, type ServiceErrorCount } from "@/features/errors";

export interface VulnSummary {
  fixableCritical: number;
  critical: number;
  high: number;
}

export interface CanvasVulnSummaries {
  services: Record<string, VulnSummary>;
  addons: Record<string, VulnSummary>;
}

// Same scoping as features/vulns canvasVulnBadges (service images in the
// selected env group, an env-less report counts as production), but
// keeps critical/high totals so the badge tooltip can say more than one
// number. Several images per owner (sidecars, init containers) take the
// per-field max: the same CVE usually appears in each.
export function canvasVulnSummaries(
  images: readonly VulnImageReport[],
  project: string,
  selectedEnv: string,
): CanvasVulnSummaries {
  const out: CanvasVulnSummaries = { services: {}, addons: {} };
  const merge = (m: Record<string, VulnSummary>, key: string, s: VulnSummary) => {
    const cur = m[key];
    m[key] = cur
      ? {
          fixableCritical: Math.max(cur.fixableCritical, s.fixableCritical),
          critical: Math.max(cur.critical, s.critical),
          high: Math.max(cur.high, s.high),
        }
      : s;
  };
  for (const img of images) {
    if (img.scan?.status !== "succeeded") continue;
    const c = img.scan.counts;
    const s = { fixableCritical: c.fixableCritical, critical: c.critical, high: c.high };
    if (img.ownerKind === "service" && img.service) {
      if ((img.env || "production") !== selectedEnv) continue;
      merge(out.services, serviceShortName(project, img.service), s);
    } else if (img.ownerKind === "addon" && img.addon) {
      merge(out.addons, serviceShortName(project, img.addon), s);
    }
  }
  return out;
}

export function vulnBadgeLabel(s: VulnSummary): string {
  const n = s.fixableCritical;
  const head = `${n} fixable critical ${n === 1 ? "vulnerability" : "vulnerabilities"}`;
  return `${head} in the live image (${s.critical} critical, ${s.high} high in total)`;
}

export type { ServiceErrorCount as ErrorSummary } from "@/features/errors";

export function errorBadgeLabel(s: ServiceErrorCount): string {
  return `${errorCountLabel(s.occurrences)}: logged in the last 24 hours (${s.groups} distinct). Open the Errors tab.`;
}

// Compact count for a badge: 1234 → "1.2k".
export function compactCount(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  return `${Math.round(n / 1000)}k`;
}
