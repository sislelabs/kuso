import { describeRevision, imageReleases } from "@/features/revisions";
import type { Revision } from "@/features/revisions";
// Not re-exported from the revisions barrel.
import { changedKeys } from "@/features/revisions/previous";
import { shortSha } from "@/lib/format";

export interface DeployMarker {
  t: number;
  label: string;
  detail?: string;
}

interface BuildLike {
  id: string;
  status: string;
  branch?: string;
  commitSha?: string;
  ref?: string;
  commitMessage?: string;
  finishedAt?: string;
}

// Saved changes that don't roll the pods.
const NO_ROLLOUT_OPS = new Set(["service.domains", "environment.domains", "service.rename", "environment.create", "environment.branch"]);
const NO_ROLLOUT_KEYS = new Set(["domains", "displayName", "description"]);

function rollsPods(r: Revision): boolean {
  const keys = changedKeys(r);
  if (keys === null) return (r.changedKeys ?? []).length > 0;
  if (keys.some((k) => NO_ROLLOUT_OPS.has(k.replace(/^op:/, "")))) return false;
  return keys.some((k) => k.startsWith("op:") || !NO_ROLLOUT_KEYS.has(k));
}

// Markers closer than this are one event (a build finishing and the
// promotion it triggered).
const SAME_EVENT_MS = 60_000;

// deployMarkers: every rollout of the env the charts can mark, from the
// same records the Deployments tab lists: successful builds of the env's
// branch, image releases and rollbacks, and config changes that roll the
// pods. lastDeployAt (features/projects/facts lastDeployedAt, the "Last
// deploy" the header shows) always gets a marker, so the legend can't say
// "No deploys in range" while the header says "Last deploy 14m ago".
export function deployMarkers(input: {
  builds: readonly BuildLike[];
  revisions: readonly Revision[];
  envBranch?: string;
  lastDeployAt?: string;
}): DeployMarker[] {
  const ranked: { m: DeployMarker; rank: number }[] = [];
  const add = (at: string | undefined, rank: number, label: string, detail?: string) => {
    const t = Date.parse(at ?? "");
    if (Number.isFinite(t)) ranked.push({ m: { t, label, ...(detail ? { detail } : {}) }, rank });
  };

  const { releases, other } = imageReleases([...input.revisions], undefined);
  for (const r of releases) {
    add(r.revision.createdAt, r.source === "rollback" ? 0 : 1, `${r.source === "rollback" ? "Rollback to" : "Deploy"} ${r.tag}`, r.repository);
  }
  for (const b of input.builds) {
    if (b.status !== "succeeded") continue;
    if (input.envBranch && (b.branch ?? "") !== input.envBranch) continue;
    add(b.finishedAt, 2, `Deploy ${b.commitSha ? shortSha(b.commitSha) : (b.ref ?? b.id)}`, b.commitMessage?.split("\n")[0]);
  }
  for (const r of other) {
    if (rollsPods(r)) add(r.createdAt, 3, `Config rollout · ${describeRevision(r).label}`, r.actor ? `by ${r.actor}` : undefined);
  }
  add(input.lastDeployAt, 4, "Last deploy", "When the version serving now went live");

  ranked.sort((a, b) => a.rank - b.rank || a.m.t - b.m.t);
  const kept: DeployMarker[] = [];
  for (const { m } of ranked) {
    if (!kept.some((k) => Math.abs(k.t - m.t) < SAME_EVENT_MS)) kept.push(m);
  }
  return kept.sort((a, b) => a.t - b.t);
}
