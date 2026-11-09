import { beforeEach, describe, expect, it, vi } from "vitest";
import type { KusoService } from "@/types/projects";

const api = vi.hoisted(() => ({
  getService: vi.fn(),
  patchService: vi.fn(),
  setServiceEnvValue: vi.fn(),
  unsetServiceEnvVar: vi.fn(),
  triggerBuild: vi.fn(),
  applyServiceChanges: vi.fn(),
}));
vi.mock("@/features/services/api", () => api);
vi.mock("@/features/staged", () => ({ applyServiceChanges: api.applyServiceChanges }));

import { applyStagedService } from "./applyStaged";

const svc = {
  spec: { port: 8080, runtime: "dockerfile", repo: { url: "https://github.com/a/b", defaultBranch: "main" } },
} as unknown as KusoService;

describe("applyStagedService", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    api.getService.mockResolvedValue(svc);
    api.applyServiceChanges.mockResolvedValue({ results: [{ kind: "patch", ok: true }], rolledOut: true });
    api.triggerBuild.mockResolvedValue({ id: "b1" });
  });

  it("starts a build after applying a branch change", async () => {
    const out = await applyStagedService("p", "api", { settings: { fields: { repoBranch: "dev" }, base: { repoBranch: "main" } } }, "");
    expect(api.applyServiceChanges).toHaveBeenCalledOnce();
    expect(api.triggerBuild).toHaveBeenCalledWith("p", "api");
    expect(out.build).toEqual({ ok: true, result: { id: "b1" } });
  });

  it("doesn't build a change that only restarts pods", async () => {
    const out = await applyStagedService("p", "api", { settings: { fields: { port: "9090" }, base: { port: "8080" } } }, "");
    expect(api.triggerBuild).not.toHaveBeenCalled();
    expect(out.build).toBeUndefined();
  });

  it("doesn't build when the apply failed", async () => {
    api.applyServiceChanges.mockResolvedValue({ results: [{ kind: "patch", ok: false, error: "nope" }], rolledOut: false });
    await applyStagedService("p", "api", { settings: { fields: { repoBranch: "dev" }, base: { repoBranch: "main" } } }, "");
    expect(api.triggerBuild).not.toHaveBeenCalled();
  });
});
