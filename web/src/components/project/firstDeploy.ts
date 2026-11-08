import type { KusoEnvironment, KusoService } from "@/types/projects";
import { deriveServiceStatus, lastDeployAt, serviceUrl, type BuildSummary } from "@/features/services";
import { serviceShortName } from "@/lib/utils";

export type StepState = "done" | "active" | "failed" | "todo";

export interface FirstDeployStep {
  id: "source" | "build" | "healthy" | "live";
  label: string;
  state: StepState;
  detail?: string;
}

export interface FirstDeployProgress {
  service: string;
  displayName: string;
  url?: string;
  steps: FirstDeployStep[];
  live: boolean;
  failed: boolean;
}

export interface ServiceSnapshot {
  service: KusoService;
  env?: KusoEnvironment;
  build?: BuildSummary;
}

const IN_FLIGHT = ["queued", "pending", "running", "deploying"];
const FAILED = ["failed", "error", "cancelled", "release-failed"];

// neverLive: the service has no deploy history and isn't serving. A
// stopped or sleeping service has been live before, so it doesn't count.
function neverLive(s: ServiceSnapshot): boolean {
  if (s.service.spec.stopped) return false;
  if (lastDeployAt(s.env, s.build)) return false;
  const kind = deriveServiceStatus({ env: s.env, latestBuild: s.build }).kind;
  return !["live", "degraded", "sleeping", "stopped"].includes(kind);
}

// pickFirstDeploy returns the service a project's "first URL" checklist
// follows, or null when the project already has something live (or has
// nothing that could go live). Public services win over internal ones;
// ties break by name so the pick is stable across polls.
export function pickFirstDeploy(project: string, snaps: readonly ServiceSnapshot[]): ServiceSnapshot | null {
  if (snaps.length === 0) return null;
  if (snaps.some((s) => !neverLive(s) && !s.service.spec.stopped)) return null;
  const candidates = snaps.filter(neverLive);
  if (candidates.length === 0) return null;
  const name = (s: ServiceSnapshot) => serviceShortName(project, s.service.metadata.name);
  return [...candidates].sort((a, b) => {
    const pa = a.service.spec.internal ? 1 : 0;
    const pb = b.service.spec.internal ? 1 : 0;
    return pa - pb || name(a).localeCompare(name(b));
  })[0];
}

// firstDeployProgress turns one service's live state into the four-step
// "repo → build → healthy → live" checklist. Image services skip the
// build step: kuso pulls them directly.
export function firstDeployProgress(project: string, s: ServiceSnapshot): FirstDeployProgress {
  const short = serviceShortName(project, s.service.metadata.name);
  const spec = s.service.spec;
  const status = deriveServiceStatus({ stopped: !!spec.stopped, env: s.env, latestBuild: s.build });
  const url = serviceUrl(s.service, s.env);
  const isImage = spec.runtime === "image";
  const build = (s.build?.status ?? "").toLowerCase();
  const steps: FirstDeployStep[] = [];

  const branch = s.env?.spec.branch || spec.repo?.defaultBranch;
  const repo = spec.repo?.url?.replace(/^https?:\/\/(www\.)?github\.com\//, "").replace(/\.git$/, "");
  const sourceDone = isImage ? !!spec.image?.repository : !!spec.repo?.url;
  steps.push({
    id: "source",
    label: isImage ? "Image set" : "Repo connected",
    state: sourceDone ? "done" : "todo",
    detail: isImage
      ? spec.image?.repository
        ? `${spec.image.repository}${spec.image.tag ? `:${spec.image.tag}` : ""}`
        : undefined
      : repo
        ? `${repo}${branch ? ` · ${branch}` : ""}`
        : undefined,
  });

  let buildDone = isImage;
  if (!isImage) {
    let state: StepState = "todo";
    let detail: string | undefined;
    if (build === "succeeded" || s.env?.status?.imageTag) {
      state = "done";
      buildDone = true;
    } else if (IN_FLIGHT.includes(build)) {
      state = "active";
      detail =
        build === "queued" && s.build?.queuePosition
          ? `Queued, #${s.build.queuePosition} in line`
          : build === "queued"
            ? "Queued"
            : "Building";
    } else if (FAILED.includes(build)) {
      state = "failed";
      detail = build === "release-failed" ? "The release command failed" : "The build failed";
    } else if (sourceDone) {
      detail = "Waiting for the first build";
    }
    steps.push({ id: "build", label: "First build", state, detail });
  }

  const healthy: FirstDeployStep = { id: "healthy", label: "Healthy", state: "todo" };
  if (buildDone) {
    if (status.kind === "live" || status.kind === "degraded") healthy.state = "done";
    else if (status.tone === "error") {
      healthy.state = "failed";
      healthy.detail = status.detail ?? "Pods aren't becoming ready";
    } else {
      healthy.state = "active";
      healthy.detail = "Starting pods and waiting for the health check";
    }
  }
  steps.push(healthy);

  const isLive = status.kind === "live" || status.kind === "degraded";
  steps.push({
    id: "live",
    label: url ? "Live" : "Running",
    state: isLive ? "done" : "todo",
    detail: url ? url.replace(/^https?:\/\//, "") : spec.internal ? "Internal only, no public URL" : undefined,
  });

  return {
    service: short,
    displayName: spec.displayName?.trim() || short,
    url,
    steps,
    live: isLive,
    failed: steps.some((st) => st.state === "failed"),
  };
}
