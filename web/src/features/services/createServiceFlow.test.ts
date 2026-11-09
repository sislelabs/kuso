import { describe, expect, it, vi } from "vitest";
import { createServiceWithEnv, type CreateServiceDeps } from "./createServiceFlow";
import type { CreateServiceBody } from "./createServiceBody";

const body: CreateServiceBody = {
  name: "api",
  displayName: "api",
  runtime: "dockerfile",
  repo: { url: "https://github.com/x/y", defaultBranch: "main" },
};

function deps(created: unknown) {
  const d = {
    post: vi.fn<CreateServiceDeps["post"]>().mockResolvedValue(created),
    trigger: vi.fn<CreateServiceDeps["trigger"]>().mockResolvedValue(undefined),
  };
  return d;
}

describe("createServiceWithEnv deploy option", () => {
  it("asks the server to skip the first build and never triggers one", async () => {
    const d = deps({ firstBuild: { triggered: false, skipped: true } });
    const out = await createServiceWithEnv("demo", body, { deploy: false }, d);
    expect(d.post).toHaveBeenCalledWith("demo", { ...body, skipFirstBuild: true });
    expect(d.trigger).not.toHaveBeenCalled();
    expect(out.build).toBe("skipped");
  });

  it("doesn't fall back to a client build on an old server that ignores the flag", async () => {
    const d = deps({});
    const out = await createServiceWithEnv("demo", body, { deploy: false }, d);
    expect(d.trigger).not.toHaveBeenCalled();
    expect(out.build).toBe("skipped");
  });

  it("reports a build an old server started despite the flag", async () => {
    const d = deps({ firstBuild: { triggered: true } });
    const out = await createServiceWithEnv("demo", body, { deploy: false }, d);
    expect(out.build).toBe("started");
  });

  it("deploys by default", async () => {
    const d = deps({});
    const out = await createServiceWithEnv("demo", body, {}, d);
    expect(d.post).toHaveBeenCalledWith("demo", body);
    expect(d.trigger).toHaveBeenCalledWith("demo", "api", "main");
    expect(out.build).toBe("started");
  });
});
