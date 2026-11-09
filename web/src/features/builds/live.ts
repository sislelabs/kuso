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

// servingBuildId: a promoted build isn't serving until its pods are
// ready. While a rollout is in flight the build whose image the previous
// pods still run (previousImageTag, from env.status.rollout) is the live
// one; the promoted build is still on its way in.
export function servingBuildId(
  builds: DeployBuild[],
  promotedId: string | undefined,
  previousImageTag: string | undefined,
): string | undefined {
  if (!previousImageTag) return promotedId;
  const promoted = builds.find((b) => b.id === promotedId);
  if (promoted?.imageTag === previousImageTag) return promotedId;
  return builds.find((b) => b.imageTag === previousImageTag && lower(b.status) === "succeeded")?.id ?? promotedId;
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

// buildStall is the plain reason an in-flight build isn't progressing
// (its pod can't be scheduled or can't pull the builder image), or
// undefined while it's progressing.
export function buildStall(b: Pick<DeployBuild, "stalled">, status: string): { summary: string; detail?: string } | undefined {
  if (!b.stalled || (status !== "pending" && status !== "running")) return undefined;
  return { summary: b.stalled.summary, detail: b.stalled.detail };
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

// buildRefLabel names what a build built: the commit SHA cut to `shaLen`,
// else the synthetic ref in full (cutting "main-x7k2p" would leave a
// meaningless prefix), else "".
export function buildRefLabel(b: { commitSha?: string; ref?: string }, shaLen: number): string {
  if (b.commitSha) return b.commitSha.slice(0, shaLen);
  return b.ref ?? "";
}

// previousSucceeded is the older successful build on the same branch, i.e.
// what a deploy replaced. Feeds the "compare" link on deploy rows. Builds
// arrive newest first.
export function previousSucceeded(builds: DeployBuild[], b: DeployBuild): DeployBuild | undefined {
  const idx = builds.indexOf(b);
  if (idx < 0) return undefined;
  return builds
    .slice(idx + 1)
    .find(
      (o) =>
        lower(o.status) === "succeeded" &&
        (o.branch ?? "") === (b.branch ?? "") &&
        !!o.commitSha &&
        o.commitSha !== b.commitSha,
    );
}

// rollForwardTarget is the newest successful build on the live build's
// branch that is newer than it: what "Undo rollback" redeploys.
export function rollForwardTarget(builds: DeployBuild[], live: DeployBuild): DeployBuild | undefined {
  if (!isRolledBack(builds, live)) return undefined;
  const liveT = buildTime(live);
  const liveIdx = builds.indexOf(live);
  return builds.find((b, i) => {
    if (b.id === live.id || lower(b.status) !== "succeeded" || !b.imageTag) return false;
    if ((b.branch ?? "") !== (live.branch ?? "")) return false;
    const t = buildTime(b);
    if (Number.isFinite(t) && Number.isFinite(liveT)) return t > liveT;
    return liveIdx >= 0 && i < liveIdx;
  });
}

// newerReleaseFailure is the build that should have replaced `live` but
// whose release hook (migration) failed, so the env still serves `live`.
// Only the newest attempt counts: a later success or failure on the branch
// is the more current story.
export function newerReleaseFailure(builds: DeployBuild[], live: DeployBuild): DeployBuild | undefined {
  const liveT = buildTime(live);
  const liveIdx = builds.indexOf(live);
  const newer = builds.filter((b, i) => {
    if (b.id === live.id || (b.branch ?? "") !== (live.branch ?? "")) return false;
    const st = lower(b.status);
    if (st !== "succeeded" && st !== "failed" && st !== "release-failed") return false;
    const t = buildTime(b);
    if (Number.isFinite(t) && Number.isFinite(liveT)) return t > liveT;
    return liveIdx >= 0 && i < liveIdx;
  });
  const newest = newer[0];
  return newest && lower(newest.status) === "release-failed" ? newest : undefined;
}

// previousDeployTarget is what "roll back to the previous build" means for
// an env: the newest older successful build on the live build's branch
// whose image still exists, preferring a different commit (rolling back to
// an older image of the same commit changes no code).
export function previousDeployTarget(
  builds: DeployBuild[],
  group: string,
  neverReady: (b: DeployBuild) => boolean = () => false,
): { live: DeployBuild; target: DeployBuild; sameCommit: boolean } | undefined {
  const liveId = liveBuildId(builds, group);
  const live = builds.find((b) => b.id === liveId);
  if (!live) return undefined;
  const older = builds
    .slice(builds.indexOf(live) + 1)
    .filter(
      (b) =>
        lower(b.status) === "succeeded" && !!b.imageTag && (b.branch ?? "") === (live.branch ?? "") && !neverReady(b),
    );
  const other = older.find((b) => !b.commitSha || b.commitSha !== live.commitSha);
  const target = other ?? older[0];
  if (!target) return undefined;
  return { live, target, sameCommit: !other };
}

// LiveImages is the env's record of images that reached a fully ready
// rollout (the kuso.sislelabs.com/live-images annotation), recorded since
// `since`.
export interface LiveImages {
  since: number;
  refs: Set<string>;
}

// buildNeverReady: a succeeded build that went out after the env started
// recording rollouts but never reached a ready rollout there (it crashed
// on start or never passed its health check). Not a safe rollback target.
// Builds older than the record, or without a known image, are unknown.
export function buildNeverReady(
  b: DeployBuild,
  live: LiveImages | null,
  repository: string | undefined,
  liveId: string | undefined,
): boolean {
  if (!live || !repository || !b.imageTag || b.id === liveId || lower(b.status) !== "succeeded") return false;
  const t = buildTime(b);
  if (!Number.isFinite(t) || t < live.since) return false;
  return !live.refs.has(`${repository}:${b.imageTag}`);
}
