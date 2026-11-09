// Mirrors the "HTTP API (contract)" section of docs/VULN_SCANNING.md.
// Keep field names and optionality in lockstep with that spec.

export const SEVERITIES = ["CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"] as const;
export type Severity = (typeof SEVERITIES)[number];

export type VulnMode = "enforce" | "report" | "off";
export type VulnPolicy = "inherit" | VulnMode;
export type VulnOwnerKind = "service" | "addon" | "platform";
export type VulnScanStatus = "queued" | "running" | "succeeded" | "failed";

export type VulnCounts = {
  critical: number;
  high: number;
  medium: number;
  low: number;
  unknown: number;
  fixableCritical: number;
  fixableHigh: number;
};

export type VulnScanSummary = {
  id: number;
  digest: string;
  imageRef: string;
  status: VulnScanStatus;
  error?: string;
  reason: "gate" | "nightly" | "manual";
  createdAt: string;
  finishedAt?: string;
  dbUpdatedAt?: string;
  // Ignored findings are excluded from counts.
  counts: VulnCounts;
  ignoredCount: number;
};

export type VulnFinding = {
  vulnId: string;
  pkgName: string;
  pkgType: string;
  installedVersion: string;
  fixedVersion?: string;
  severity: Severity;
  title?: string;
  primaryURL?: string;
  target?: string;
  ignored: boolean;
  ignoreId?: number;
};

export type VulnIgnore = {
  id: number;
  scope: VulnOwnerKind;
  project?: string;
  service?: string;
  vulnId: string;
  reason: string;
  expiresAt?: string;
  createdBy: string;
  createdAt: string;
  expired: boolean;
};

export type VulnImageReport = {
  ownerKind: VulnOwnerKind;
  project?: string;
  service?: string;
  env?: string;
  addon?: string;
  namespace?: string;
  workload?: string;
  imageRef: string;
  digest: string;
  // Latest scan for this digest, any status.
  scan: VulnScanSummary | null;
};

export type ProjectVulnsResponse = { mode: VulnMode; images: VulnImageReport[] };

export type ServiceVulnsResponse = {
  policy: VulnPolicy;
  effectiveMode: VulnMode;
  image: VulnImageReport | null;
  findings: VulnFinding[];
};

export type AddonImageUpdateStatus =
  | "current"
  | "scanning"
  | "ready"
  | "rejected"
  | "scan-failed"
  | "updating"
  | "rolling"
  | "rolled-back"
  | "error";

export type AddonImageUpdateState = {
  // Tag reference the chart renders, e.g. "postgres:16".
  image: string;
  running?: string;
  latest?: string;
  checkedAt: string;
  status: AddonImageUpdateStatus;
  message?: string;
  // Fixable critical+high; -1 = not scanned.
  fixableRunning: number;
  fixableLatest: number;
  appliedAt?: string;
  startedAt?: string;
  manual?: boolean;
};

export type AddonImageUpdate = {
  auto: boolean;
  windowHourUTC: number;
  // False for external / instance-shared addons kuso runs no image for.
  available: boolean;
  state: AddonImageUpdateState | null;
};

export type AddonVulnsResponse = {
  image: VulnImageReport | null;
  findings: VulnFinding[];
  // Absent on servers older than the addon image updater.
  imageUpdate?: AddonImageUpdate;
};

export type VulnPolicyResponse = { mode: VulnPolicy; effectiveMode: VulnMode };

export type ScanQueuedResponse = { scanId: number };

export type PlatformVulnsResponse = { images: VulnImageReport[] };

export type PlatformFindingsResponse = { image: VulnImageReport; findings: VulnFinding[] };

export type ScanAllResponse = { queued: number };

export type VulnScanSettings = {
  mode: VulnMode;
  scanAddons: boolean;
  scanPlatform: boolean;
  // 0-23
  nightlyHourUTC: number;
  notifyMinSeverity: "critical" | "high" | "none";
  autoUpdateAddons: boolean;
};

export type AddVulnIgnoreInput = { vulnId: string; reason: string; expiresInDays?: number };

// A service or addon whose findings/ignores/rescan share one URL shape.
export type VulnOwner =
  { kind: "service"; project: string; name: string; env: string } | { kind: "addon"; project: string; name: string };
