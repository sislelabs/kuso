// One phrasing of addon backup coverage for every surface that counts it
// (settings overview tile, Backups page). The denominator is always every
// addon row GET /api/admin/backup-health returns — all addon CRs across
// projects, external and instance-shared ones included — so two surfaces
// can't quote different totals for the same fact.

export interface AddonBackupRowLike {
  covered: boolean;
  healthy: boolean;
}

export interface AddonBackupCoverage {
  total: number;
  // Has a backup CronJob kuso actually runs (directly or via the
  // instance addon that dumps its database).
  covered: number;
  uncovered: number;
  // Covered, but the latest runs aren't succeeding.
  failing: number;
}

export function summarizeAddonBackups(rows: ReadonlyArray<AddonBackupRowLike>): AddonBackupCoverage {
  let covered = 0;
  let failing = 0;
  for (const r of rows) {
    if (!r.covered) continue;
    covered++;
    if (!r.healthy) failing++;
  }
  return { total: rows.length, covered, uncovered: rows.length - covered, failing };
}

const addons = (n: number) => (n === 1 ? "addon" : "addons");

// "24 of 59 addons have no scheduled backup" / "All 3 addons have a
// scheduled backup".
export function addonBackupCoverageText(c: AddonBackupCoverage): string {
  if (c.total === 0) return "No addons to back up";
  if (c.uncovered === 0) return c.total === 1 ? "The 1 addon has a scheduled backup" : `All ${c.total} addons have a scheduled backup`;
  return `${c.uncovered} of ${c.total} ${addons(c.total)} ${c.uncovered === 1 ? "has" : "have"} no scheduled backup`;
}

// "2 of 35 scheduled addon backups failing"; undefined when none fail.
export function addonBackupFailingText(c: AddonBackupCoverage): string | undefined {
  if (c.failing === 0) return undefined;
  return `${c.failing} of ${c.covered} scheduled addon ${c.covered === 1 ? "backup" : "backups"} failing`;
}
