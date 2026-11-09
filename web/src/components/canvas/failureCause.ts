import { failedStepFromBuild } from "@/features/builds";
import type { BuildSummary, NodeStatus } from "@/features/services";
import { imageTag, pullProblem, rolloutFacts, stuckFacts } from "@/lib/status";

// The server's failure classes, in the few words a canvas card has room for.
const KIND_CAUSE: Record<string, string> = {
  clone_ref_missing: "Branch not found",
  clone_unreachable: "Can't reach the repository",
  dockerfile_not_found: "Dockerfile not found",
  dockerfile_missing_copy: "A copied file is missing",
  lockfile_drift: "Lockfile out of date",
  dependency_resolution: "Dependency install failed",
  build_command_failed: "Build command failed",
  build_oom: "Build ran out of memory",
  missing_build_arg: "Missing build variable",
  registry_auth: "Registry denied access",
  release_failed: "Release command failed",
  missing_env: "Missing environment variable",
  oom: "Out of memory",
  crash_loop: "Crashes on start",
  image_pull_failed: "Image pull failed",
  port_conflict: "Port already in use",
  healthcheck_failed: "Health check failing",
  migration_lock: "Migration lock timed out",
  missing_capability: "Image needs a capability",
  "vuln-blocked": "Blocked by critical CVEs",
};

const ROLLOUT_CAUSE: Record<string, string> = {
  crash: "Crashes on start",
  image_pull: "Image pull failed",
  config_error: "Config error",
  stalled: "Rollout stalled",
};

type EnvStatus = Record<string, unknown> | undefined;

// failureCause is the one-line reason a failing card shows under its
// name ("Branch not found", "Health check 404"), like Render's tiles.
// Undefined when the card isn't failing or nothing names a cause.
export function failureCause(status: NodeStatus, envStatus: EnvStatus, build?: BuildSummary): string | undefined {
  if (status.tone !== "error" && status.kind !== "rollout_failing") return undefined;

  const rollout = rolloutFacts(envStatus);
  if (rollout?.kind === "healthcheck") {
    return rollout.probeResult ? `Health check ${rollout.probeResult}` : "Health check failing";
  }
  if (rollout?.kind === "image_pull" && rollout.pullCause) {
    const short = pullProblem(rollout.pullCause, imageTag(rollout.newImage)).short;
    return short.charAt(0).toUpperCase() + short.slice(1);
  }
  if (rollout?.kind && ROLLOUT_CAUSE[rollout.kind]) return ROLLOUT_CAUSE[rollout.kind];

  const stuck = stuckFacts(envStatus);
  if (stuck?.summary) return stuck.summary;

  if (status.kind === "build_failed" && build) {
    const kind = build.failureClass?.kind;
    if (kind && KIND_CAUSE[kind]) return KIND_CAUSE[kind];
    const step = failedStepFromBuild(build);
    if (step) return `${step} failed`;
    return "Build failed";
  }
  if (rollout?.cause) return rollout.cause;
  return status.detail || undefined;
}
