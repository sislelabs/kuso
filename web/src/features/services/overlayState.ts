// Pure decision helpers for the service overlay header + day-2 panels.
// Kept out of the components so the branchy bits have unit tests.

import type { BuildSummary } from "./api";

function buildTime(b: BuildSummary): number {
  const t = Date.parse(b.finishedAt ?? b.startedAt ?? "");
  return Number.isFinite(t) ? t : 0;
}

export interface LiveRef {
  // env.status.imageTag / env.status.commit — whichever the server stamped.
  imageTag?: string;
  commit?: string;
  // env.spec.branch. The server rejects a rollback to a build from
  // another branch, so the target must match.
  branch?: string;
}

// findLiveBuild matches the env's running image back to its build.
export function findLiveBuild(builds: BuildSummary[], live: LiveRef): BuildSummary | undefined {
  if (live.imageTag) {
    const byTag = builds.find((b) => b.imageTag && b.imageTag === live.imageTag);
    if (byTag) return byTag;
  }
  if (live.commit) {
    const succeeded = builds.filter((b) => b.status === "succeeded" && b.commitSha);
    return succeeded.find((b) => b.commitSha === live.commit || b.commitSha!.startsWith(live.commit!));
  }
  return undefined;
}

// pickRollbackTarget returns the newest succeeded build on the env's
// branch that is strictly older than the live build. When the live build
// can't be identified we assume the newest succeeded build on the branch
// is live and step one back — never onto the live build itself.
export function pickRollbackTarget(builds: BuildSummary[], live: LiveRef): BuildSummary | undefined {
  const liveBuild = findLiveBuild(builds, live);
  const branch = live.branch || liveBuild?.branch;
  const candidates = builds
    .filter((b) => b.status === "succeeded")
    .filter((b) => !branch || !b.branch || b.branch === branch)
    .sort((a, b) => buildTime(b) - buildTime(a));
  if (!liveBuild) return candidates[1];
  const liveAt = buildTime(liveBuild);
  return candidates.find(
    (b) =>
      b.id !== liveBuild.id &&
      buildTime(b) < liveAt &&
      (!liveBuild.imageTag || b.imageTag !== liveBuild.imageTag),
  );
}

// Server rollup states (env.status.state) that mean "the running pods
// are unhealthy". build_failed / release_failed are different: the
// last green version keeps serving, so restart/rollback don't apply.
export type HealthProblem = "runtime" | "deploy" | null;

export function healthProblem(serverState: string | undefined, uiStatus: string): HealthProblem {
  if (serverState === "build_failed" || serverState === "release_failed") return "deploy";
  if (serverState === "crashlooping" || serverState === "degraded") return "runtime";
  if (!serverState && uiStatus === "failed") return "runtime";
  return null;
}

export interface DriftLike {
  podsStale?: string[];
  rolloutPending?: boolean;
  specPending?: string[];
  helmError?: string;
}

// needsRestart: pods run an older config and nothing is rolling them.
// Only for a settled, healthy env — a stuck rollout (crashloop, or an
// image pull that leaves the state at "deploying") also leaves pods
// stale, but a restart won't fix that.
export function needsRestart(drift: DriftLike | undefined, serverState: string | undefined): boolean {
  if (!drift) return false;
  if (serverState && serverState !== "running") return false;
  if (drift.rolloutPending) return false;
  if (drift.helmError) return false;
  if (drift.specPending && drift.specPending.length > 0) return false;
  return !!drift.podsStale && drift.podsStale.length > 0;
}

const MEM_UNITS: Record<string, number> = {
  "": 1,
  k: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  Ki: 1024,
  Mi: 1024 ** 2,
  Gi: 1024 ** 3,
  Ti: 1024 ** 4,
};

// parseMemoryQuantity turns a k8s memory quantity ("512Mi", "1G",
// "134217728") into bytes. Undefined for blank or unparseable input.
export function parseMemoryQuantity(q: string | undefined): number | undefined {
  if (!q) return undefined;
  const m = /^\s*([0-9]+(?:\.[0-9]+)?)\s*(Ki|Mi|Gi|Ti|k|M|G|T)?\s*$/.exec(q);
  if (!m) return undefined;
  const n = Number(m[1]) * MEM_UNITS[m[2] ?? ""];
  return n > 0 ? n : undefined;
}

export const MEMORY_WARN_RATIO = 0.85;

// memoryPressure: the highest per-pod used/limit ratio. Undefined when
// there is no limit to compare against.
export function memoryPressure(podBytes: number[], limitBytes: number | undefined): number | undefined {
  if (!limitBytes || podBytes.length === 0) return undefined;
  return Math.max(...podBytes) / limitBytes;
}

export interface PodSizeLike {
  Name: string;
  CPURequest: string;
  CPULimit: string;
  MemoryRequest: string;
  MemoryLimit: string;
}

export interface ResourceFields {
  cpuRequest: string;
  cpuLimit: string;
  memRequest: string;
  memLimit: string;
}

// matchPodSize names the preset whose four values equal the form's, or
// "" when all four are blank (cluster default), or null for custom.
export function matchPodSize(presets: PodSizeLike[], r: ResourceFields): string | null {
  const vals = [r.cpuRequest, r.cpuLimit, r.memRequest, r.memLimit].map((v) => v.trim());
  if (vals.every((v) => v === "")) return "";
  const hit = presets.find(
    (p) =>
      p.CPURequest === vals[0] &&
      p.CPULimit === vals[1] &&
      p.MemoryRequest === vals[2] &&
      p.MemoryLimit === vals[3],
  );
  return hit ? hit.Name : null;
}

export function podSizeFields(p: PodSizeLike): ResourceFields {
  return {
    cpuRequest: p.CPURequest,
    cpuLimit: p.CPULimit,
    memRequest: p.MemoryRequest,
    memLimit: p.MemoryLimit,
  };
}
