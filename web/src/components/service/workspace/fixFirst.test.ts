import { describe, expect, it } from "vitest";
import type { BuildSummary } from "@/features/services";
import type { KusoEnvironment } from "@/types/projects";
import { fixFirstAction } from "./fixFirst";

const env = (status: Record<string, unknown>) => ({ metadata: { name: "e" }, spec: {}, status }) as unknown as KusoEnvironment;
const build = (b: Partial<BuildSummary>) => ({ id: "b", serviceName: "s", status: "failed", ...b }) as BuildSummary;

describe("fixFirstAction", () => {
  it("sends a failing health check to the health check settings", () => {
    const fix = fixFirstAction({
      env: env({ state: "rollout_failing", rollout: { kind: "healthcheck", previousServing: true } }),
      latestBuild: undefined,
      isImage: true,
    });
    expect(fix).toMatchObject({ id: "healthcheck", label: "Fix health check", anchor: "healthcheck" });
  });
  it("offers a new image when the image can't be pulled", () => {
    const e = env({ state: "stuck", stuck: { reason: "image_pull", summary: "can't pull" } });
    expect(fixFirstAction({ env: e, latestBuild: undefined, isImage: true })?.id).toBe("image");
    // A built image that fails to pull isn't fixed by picking a tag.
    expect(fixFirstAction({ env: e, latestBuild: undefined, isImage: false })).toBeUndefined();
  });
  it("offers a branch change when the branch is gone", () => {
    const b = build({ errorMessage: "fatal: Remote branch nope not found in upstream origin" });
    expect(fixFirstAction({ env: env({ state: "build_failed" }), latestBuild: b, isImage: false })?.label).toBe("Change branch");
    const classified = build({ failureClass: { kind: "clone_ref_missing", tab: "build", summary: "" } as BuildSummary["failureClass"] });
    expect(fixFirstAction({ env: env({ state: "build_failed" }), latestBuild: classified, isImage: false })?.id).toBe("branch");
  });
  it("has no fix for an ordinary failed build or a healthy env", () => {
    expect(fixFirstAction({ env: env({ state: "build_failed" }), latestBuild: build({ errorMessage: "npm ERR" }), isImage: false })).toBeUndefined();
    expect(fixFirstAction({ env: env({ state: "running" }), latestBuild: build({ status: "succeeded" }), isImage: false })).toBeUndefined();
  });
});
