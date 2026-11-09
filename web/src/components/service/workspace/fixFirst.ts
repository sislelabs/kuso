import { rolloutFacts, stuckFacts } from "@/lib/status";
import type { BuildSummary } from "@/features/services";
import type { KusoEnvironment } from "@/types/projects";

// FixFirst is the one action that clears a failure whose cause is known.
// While it applies, it replaces Deploy / Redeploy as the primary button:
// deploying again would fail the same way.
export interface FixFirst {
  id: "branch" | "healthcheck" | "image";
  label: string;
  // Where the fix lives; "image" opens the header's image picker instead.
  tab: string;
  anchor?: string;
}

// The branch is gone from the repository: a retry clones the same
// missing ref. Matches the classifier kind and, for older builds, git's
// own fatal line.
export function buildMissingBranch(b: Pick<BuildSummary, "errorMessage" | "failureClass"> | undefined): boolean {
  if (!b) return false;
  return (
    b.failureClass?.kind === "clone_ref_missing" ||
    /Remote branch \S+ not found|couldn't find remote ref/i.test(`${b.errorMessage ?? ""} ${b.failureClass?.lineHint ?? ""}`)
  );
}

export function fixFirstAction({
  env,
  latestBuild,
  isImage,
}: {
  env: KusoEnvironment | undefined;
  // The newest build for this env's branch.
  latestBuild: BuildSummary | undefined;
  isImage: boolean;
}): FixFirst | undefined {
  const state = env?.status?.state;
  if (state === "rollout_failing" && rolloutFacts(env?.status)?.kind === "healthcheck") {
    return { id: "healthcheck", label: "Fix health check", tab: "settings", anchor: "healthcheck" };
  }
  const pullFailed =
    (state === "stuck" && stuckFacts(env?.status)?.reason === "image_pull") ||
    (state === "rollout_failing" && rolloutFacts(env?.status)?.kind === "image_pull");
  if (pullFailed && isImage) return { id: "image", label: "Change image", tab: "settings", anchor: "build" };
  const failed = ["failed", "error"].includes((latestBuild?.status ?? "").toLowerCase());
  if (!isImage && failed && buildMissingBranch(latestBuild)) {
    return { id: "branch", label: "Change branch", tab: "settings", anchor: "source" };
  }
  return undefined;
}
