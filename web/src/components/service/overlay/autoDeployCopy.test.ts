import { describe, expect, it } from "vitest";
import type { KusoServiceSpec } from "@/types/projects";
import { autoDeploySwitchCopy, pushDeployState } from "./AutoDeployBanner";

const spec = { runtime: "dockerfile", repo: { url: "https://github.com/acme/api", defaultBranch: "main" } } as KusoServiceSpec;

describe("autoDeploySwitchCopy", () => {
  it("says blocked, not plain Enabled, when the GitHub App is missing", () => {
    const state = pushDeployState({ spec, githubConfigured: false })!;
    const copy = autoDeploySwitchCopy(state);
    expect(copy.label).toBe("Enabled · blocked");
    expect(copy.blocked).toMatch(/connect the GitHub App/);
  });
  it("is Enabled when pushes really deploy, Paused when paused", () => {
    expect(autoDeploySwitchCopy(pushDeployState({ spec, githubConfigured: true })!)).toEqual({ label: "Enabled" });
    expect(autoDeploySwitchCopy(pushDeployState({ spec: { ...spec, autoDeployPaused: true }, githubConfigured: false })!).label).toBe("Paused");
  });
});
