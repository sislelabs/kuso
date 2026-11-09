import { describe, expect, it } from "vitest";
import { imageRolloutState, type RolloutPod } from "./imageRollout";

const pod = (p: Partial<RolloutPod>): RolloutPod => ({ name: "web-1", ready: false, ...p });

describe("imageRolloutState", () => {
  it("waits for data", () => {
    expect(imageRolloutState("deploying", undefined)).toBeUndefined();
  });
  it("walks the stages", () => {
    expect(imageRolloutState("deploying", [])?.step).toBe("scheduling");
    expect(imageRolloutState("deploying", [pod({ phase: "Pending", reason: "Unschedulable", message: "0/3 nodes" })])).toMatchObject({
      step: "scheduling",
      detail: "0/3 nodes",
    });
    expect(imageRolloutState("deploying", [pod({ phase: "Pending", reason: "ContainerCreating" })])?.step).toBe("pulling");
    expect(imageRolloutState("deploying", [pod({ phase: "Running" })])?.step).toBe("starting");
    expect(imageRolloutState("running", [pod({ ready: true })])).toMatchObject({ step: "ready", status: "done" });
  });
  it("names pull and crash failures", () => {
    expect(imageRolloutState("deploying", [pod({ reason: "ImagePullBackOff", message: "not found" })])).toMatchObject({
      step: "pulling",
      status: "failed",
      detail: "not found",
    });
    expect(imageRolloutState("crashlooping", [pod({ phase: "Running", reason: "CrashLoopBackOff" })])).toMatchObject({
      step: "starting",
      status: "failed",
    });
  });
  it("treats stopped and asleep as idle", () => {
    expect(imageRolloutState("stopped", [])?.status).toBe("idle");
    expect(imageRolloutState("sleeping", undefined)?.status).toBe("idle");
  });
  it("counts only current pods; a draining old ReplicaSet isn't part of the rollout", () => {
    const st = imageRolloutState("running", [pod({ name: "web-new", ready: true }), pod({ name: "web-old", ready: false, current: false })]);
    expect(st).toMatchObject({ status: "done", ready: 1, total: 1 });
  });
  it("reads a stuck unschedulable rollout as failed with the plain summary", () => {
    const st = imageRolloutState("stuck", [pod({ phase: "Pending", reason: "Unschedulable", message: "0/3 nodes are available", summary: "No node has enough memory" })]);
    expect(st).toMatchObject({ status: "failed", detail: "No node has enough memory" });
  });
});
