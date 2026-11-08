import type { ServicePod } from "@/features/pods";

// The service pods endpoint also reports why a pod isn't ready; older
// servers omit these fields.
export type RolloutPod = ServicePod & { reason?: string; message?: string; restarts?: number };

export type RolloutStep = "scheduling" | "pulling" | "starting" | "ready";

export const ROLLOUT_STEPS: { step: RolloutStep; label: string }[] = [
  { step: "scheduling", label: "Scheduled" },
  { step: "pulling", label: "Image pulled" },
  { step: "starting", label: "Started" },
  { step: "ready", label: "Ready" },
];

export interface RolloutState {
  // The step in progress (or reached, when status is "done").
  step: RolloutStep;
  status: "active" | "failed" | "done" | "idle";
  title: string;
  detail?: string;
  ready: number;
  total: number;
}

const PULL_FAILED = new Set(["ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull", "RegistryUnavailable"]);
const START_FAILED = new Set(["CrashLoopBackOff", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "Error", "OOMKilled"]);
const PULLING = new Set(["ContainerCreating", "PodInitializing"]);

// imageRolloutState folds env state + pod status into one rollout stage
// for services that deploy a registry image (no build to show).
export function imageRolloutState(envState: string | undefined, pods: readonly RolloutPod[] | undefined): RolloutState | undefined {
  if (envState === "stopped") return { step: "scheduling", status: "idle", title: "Stopped", detail: "Start the service to roll it out again.", ready: 0, total: 0 };
  if (envState === "sleeping") return { step: "ready", status: "idle", title: "Asleep", detail: "It wakes on the next request.", ready: 0, total: 0 };
  if (!pods) return undefined;
  const total = pods.length;
  const ready = pods.filter((p) => p.ready).length;
  if (total > 0 && ready === total) return { step: "ready", status: "done", title: "Running", ready, total };

  const notReady = pods.filter((p) => !p.ready);
  const pullFail = notReady.find((p) => p.reason && PULL_FAILED.has(p.reason));
  if (pullFail) {
    return { step: "pulling", status: "failed", title: "Can't pull the image", detail: pullFail.message || pullFail.reason, ready, total };
  }
  const startFail = notReady.find((p) => p.reason && START_FAILED.has(p.reason));
  if (startFail || envState === "crashlooping") {
    return {
      step: "starting",
      status: "failed",
      title: "The container keeps crashing",
      detail: startFail?.message || startFail?.reason || "Check the Logs tab for the error it exits with.",
      ready,
      total,
    };
  }
  if (total === 0) {
    return { step: "scheduling", status: "active", title: "Waiting for a pod", detail: "The deployment is being created.", ready, total };
  }
  const unscheduled = notReady.find((p) => p.reason === "Unschedulable");
  if (unscheduled) {
    return { step: "scheduling", status: "active", title: "Waiting for room on a node", detail: unscheduled.message, ready, total };
  }
  if (notReady.some((p) => (p.reason && PULLING.has(p.reason)) || (p.phase === "Pending" && !p.reason))) {
    return { step: "pulling", status: "active", title: "Pulling the image", detail: "Large images can take a minute on first deploy.", ready, total };
  }
  return { step: "starting", status: "active", title: "Starting", detail: "Waiting for the container to pass its health check.", ready, total };
}
