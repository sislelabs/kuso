import type { BuildRowStatus, DeployBuild } from "./types";

const lower = (s: string | undefined) => (s ?? "").toLowerCase();

function buildTime(b: DeployBuild): number {
  const t = Date.parse(b.finishedAt ?? b.startedAt ?? "");
  return Number.isFinite(t) ? t : NaN;
}

// hasLiveInfo: the server reports promotion state via liveEnvs. Older
// servers never send the field, so its absence on every build means
// we must fall back to image-tag matching.
export function hasLiveInfo(builds: DeployBuild[]): boolean {
  return builds.some((b) => Array.isArray(b.liveEnvs));
}

// liveBuildId picks the build currently serving `group`. With liveEnvs
// it's authoritative; otherwise the build whose imageTag matches the
// env's tag, else the newest succeeded build (builds arrive newest-first).
export function liveBuildId(
  builds: DeployBuild[],
  group: string,
  envImageTag?: string,
): string | undefined {
  if (hasLiveInfo(builds)) {
    return builds.find((b) => b.liveEnvs?.includes(group))?.id;
  }
  const succeeded = builds.filter((b) => lower(b.status) === "succeeded");
  if (envImageTag) return succeeded.find((b) => b.imageTag === envImageTag)?.id;
  return succeeded[0]?.id;
}

export function classifyBuild(b: DeployBuild, liveId: string | undefined): BuildRowStatus {
  const s = lower(b.status);
  if (s === "succeeded") return b.id === liveId ? "active" : "superseded";
  if (s === "failed") return "failed";
  // Built fine, but the release hook (migration) failed so it was never promoted.
  if (s === "release-failed") return "release-failed";
  if (s === "running") return "running";
  if (s === "pending") return "pending";
  if (s === "queued") return "queued";
  if (s === "cancelled") return "cancelled";
  return "unknown";
}

// isRolledBack: a newer succeeded build exists on the live build's
// branch, so the env is pinned to an older image and the next push will
// deploy over it.
export function isRolledBack(builds: DeployBuild[], live: DeployBuild): boolean {
  const liveIdx = builds.indexOf(live);
  const liveT = buildTime(live);
  return builds.some((b, i) => {
    if (b.id === live.id || lower(b.status) !== "succeeded") return false;
    if ((b.branch ?? "") !== (live.branch ?? "")) return false;
    const t = buildTime(b);
    if (Number.isFinite(t) && Number.isFinite(liveT)) return t > liveT;
    return liveIdx >= 0 && i < liveIdx;
  });
}

// buildNote is the one muted line explaining why a build is where it is.
export function buildNote(b: DeployBuild, status: BuildRowStatus): string {
  if (status === "queued" || status === "pending") {
    if (b.waitingFor) return waitingLabel(b.waitingFor);
  }
  if (status === "cancelled" && b.cancelReason) return b.cancelReason;
  if (b.notPromotedReason) return b.notPromotedReason;
  if (b.waitingFor && (status === "running" || status === "unknown")) {
    return waitingLabel(b.waitingFor);
  }
  return "";
}

const CI_CODES = new Set(["ci", "checks", "ci-checks", "github-checks", "github-ci"]);

export function waitingLabel(waitingFor: string): string {
  const w = waitingFor.trim();
  if (CI_CODES.has(w.toLowerCase())) return "Waiting for GitHub CI checks";
  if (/^waiting\b/i.test(w)) return w.charAt(0).toUpperCase() + w.slice(1);
  return `Waiting for ${w}`;
}

// isBranchMismatch recognises the rollback 400 the server returns when
// the target build's branch differs from the env's branch — the only
// 400 that "Roll back anyway" (force) can get past.
export function isBranchMismatch(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  const status = (err as { status?: unknown }).status;
  return status === 400 && /branch/i.test(err.message);
}
