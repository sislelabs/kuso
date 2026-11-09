import type { BuildSummary } from "@/features/services";
import type { KusoEnvironment } from "@/types/projects";
import { lastDeployedAt } from "@/features/projects/facts";

// FirstRun is the one reading of "has this service ever run here?" that
// the header, health strip, Pods, Logs and Metrics all share, so a service
// whose first build failed never shows "0 restarts · Uptime OK".
export type FirstRun =
  | { kind: "ran" }
  | {
      kind: "never";
      // Newest build for this environment's branch, if any.
      lastBuild?: BuildSummary;
      failed: boolean;
      building: boolean;
    };

const IN_FLIGHT = new Set(["queued", "pending", "running", "deploying"]);
const FAILED = new Set(["failed", "error", "release-failed"]);

export function firstRunState(
  env: KusoEnvironment | undefined,
  builds: readonly BuildSummary[] | undefined,
): FirstRun {
  if (!env) return { kind: "ran" };
  const st = env.status;
  // The server's rollup says it outright; otherwise an image or a
  // promotion stamp means something ran.
  if (st?.state !== "no_image" && (!!st?.imageTag || !!lastDeployedAt(env))) return { kind: "ran" };
  if (st?.state && st.state !== "no_image" && st.state !== "build_failed" && st.state !== "building") {
    return { kind: "ran" };
  }
  const branch = env.spec?.branch;
  const mine = (builds ?? [])
    .filter((b) => !branch || !b.branch || b.branch === branch)
    .sort((a, b) => (b.startedAt ?? "").localeCompare(a.startedAt ?? ""));
  if (mine.some((b) => b.status?.toLowerCase() === "succeeded" && (b.liveEnvs?.length ?? 0) > 0)) return { kind: "ran" };
  const lastBuild = mine[0];
  const s = lastBuild?.status?.toLowerCase() ?? "";
  return { kind: "never", lastBuild, failed: FAILED.has(s), building: IN_FLIGHT.has(s) };
}
