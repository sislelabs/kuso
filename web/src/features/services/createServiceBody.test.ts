import { describe, expect, it, vi } from "vitest";
import { buildCreateServiceBody, createPortProblem, followsProjectRepo } from "./createServiceBody";
import { createServiceWithEnv, type CreateServiceDeps } from "./createServiceFlow";

const repo = { url: "https://github.com/acme/shop", branch: "main", installationId: 7 };

describe("buildCreateServiceBody", () => {
  it("maps an App-picked repo service, pinning a non-project branch", () => {
    expect(
      buildCreateServiceBody({
        source: "repo",
        name: " Shop API ",
        repo: { ...repo, branch: "develop", path: " apps/api " },
        projectRepo: { url: "https://github.com/acme/shop", defaultBranch: "main" },
        kind: "web",
        runtime: "dockerfile",
        dockerfile: " docker/Dockerfile ",
        command: "ignored for web",
        port: "3000",
        sizing: { scale: { min: 2 } },
      }),
    ).toEqual({
      name: "shop-api",
      displayName: "Shop API",
      runtime: "dockerfile",
      repo: { url: "https://github.com/acme/shop", defaultBranch: "develop", path: "apps/api" },
      dockerfile: "docker/Dockerfile",
      port: 3000,
      github: { installationId: 7 },
      scale: { min: 2 },
    });
  });

  it("leaves the branch unset when the service follows the project repo", () => {
    const body = buildCreateServiceBody({
      source: "repo",
      name: "shop",
      repo: { ...repo, url: "https://github.com/Acme/shop.git/" },
      projectRepo: { url: "https://github.com/acme/shop" },
      kind: "web",
      runtime: "nixpacks",
      port: "auto",
    });
    expect(body.repo).toEqual({ url: "https://github.com/Acme/shop.git/" });
    expect(body).not.toHaveProperty("port");
  });

  it("sends a pasted repo's token and no installation", () => {
    const body = buildCreateServiceBody({
      source: "repo",
      name: "web",
      repo: { url: "https://gitlab.com/a/b", branch: "main", token: "glpat-x", installationId: 0 },
      kind: "web",
      runtime: "nixpacks",
    });
    expect(body.repo).toEqual({ url: "https://gitlab.com/a/b", defaultBranch: "main", token: "glpat-x" });
    expect(body).not.toHaveProperty("github");
  });

  it("shapes a worker: fromService + argv, no port or dockerfile", () => {
    const body = buildCreateServiceBody({
      source: "repo",
      name: "jobs",
      repo,
      kind: "worker",
      runtime: "dockerfile",
      dockerfile: "Dockerfile.x",
      command: "  bundle exec   sidekiq ",
      fromService: "web",
      port: "8080",
    });
    expect(body.runtime).toBe("worker");
    expect(body.command).toEqual(["bundle", "exec", "sidekiq"]);
    expect(body.fromService).toBe("web");
    expect(body).not.toHaveProperty("port");
    expect(body).not.toHaveProperty("dockerfile");
  });

  it("shapes an image service with a default tag", () => {
    expect(
      buildCreateServiceBody({ source: "image", name: "Proxy", image: " nginx ", tag: " ", pullSecret: "", port: "80" }),
    ).toEqual({
      name: "proxy",
      displayName: "Proxy",
      runtime: "image",
      image: { repository: "nginx", tag: "latest" },
      port: 80,
    });
  });
});

describe("followsProjectRepo", () => {
  it("treats a project repo with no branch as main", () => {
    expect(followsProjectRepo({ url: "https://github.com/a/b" }, "https://github.com/a/b", "main")).toBe(true);
    expect(followsProjectRepo({ url: "https://github.com/a/b" }, "https://github.com/a/b", "dev")).toBe(false);
    expect(followsProjectRepo(null, "https://github.com/a/b", "main")).toBe(false);
  });
});

function deps(created: unknown, opts: { failTrigger?: boolean } = {}) {
  const calls: string[] = [];
  const d: CreateServiceDeps = {
    post: vi.fn(async () => {
      calls.push("post");
      return created;
    }),
    trigger: vi.fn(async () => {
      calls.push("trigger");
      if (opts.failTrigger) throw new Error("no builder");
    }),
  };
  return { d, calls };
}

const repoBody = buildCreateServiceBody({ source: "repo", name: "web", repo, kind: "web", runtime: "nixpacks" });

describe("createServiceWithEnv", () => {
  it("sends variables and the addon subscription in the one create call", async () => {
    const { d, calls } = deps({ firstBuild: { triggered: true } });
    const out = await createServiceWithEnv(
      "p",
      repoBody,
      { envVars: [{ name: "A", value: "1" }, { name: "B", value: "${{ db.DATABASE_URL }}" }], subscribedAddons: ["db"] },
      d,
    );
    // One request: a follow-up write would roll the new env a second time.
    expect(calls).toEqual(["post"]);
    expect(d.post).toHaveBeenCalledWith("p", {
      ...repoBody,
      envValues: [
        { name: "A", value: "1" },
        { name: "B", value: "${{ db.DATABASE_URL }}" },
      ],
      subscribedAddons: ["db"],
    });
    expect(out).toEqual({ build: "started" });
  });

  it("keeps an empty subscription (mount no addon) and omits an absent one", async () => {
    const none = deps({});
    await createServiceWithEnv("p", repoBody, { subscribedAddons: [] }, none.d);
    expect(none.d.post).toHaveBeenCalledWith("p", { ...repoBody, subscribedAddons: [] });
    const all = deps({});
    await createServiceWithEnv("p", repoBody, { envVars: [] }, all.d);
    expect(all.d.post).toHaveBeenCalledWith("p", repoBody);
  });

  it("surfaces the server's first-build error without retrying", async () => {
    const { d, calls } = deps({ firstBuild: { triggered: false, error: "quota" } });
    expect(await createServiceWithEnv("p", repoBody, {}, d)).toEqual({ build: "failed", buildError: "quota" });
    expect(calls).toEqual(["post"]);
  });

  it("triggers the build itself on servers without firstBuild", async () => {
    const { d, calls } = deps({ metadata: {} });
    expect((await createServiceWithEnv("p", repoBody, { envVars: [{ name: "A", value: "1" }] }, d)).build).toBe("started");
    expect(calls).toEqual(["post", "trigger"]);
    expect(d.trigger).toHaveBeenCalledWith("p", "web", "main");
  });

  it("never builds an image service", async () => {
    const { d, calls } = deps(null);
    const body = buildCreateServiceBody({ source: "image", name: "x", image: "nginx", tag: "1" });
    expect((await createServiceWithEnv("p", body, {}, d)).build).toBe("none");
    expect(calls).toEqual(["post"]);
  });

  it("propagates a failed create", async () => {
    const d: CreateServiceDeps = {
      post: async () => {
        throw new Error("conflict");
      },
      trigger: vi.fn(),
    };
    await expect(createServiceWithEnv("p", repoBody, { envVars: [{ name: "A", value: "1" }] }, d)).rejects.toThrow("conflict");
    expect(d.trigger).not.toHaveBeenCalled();
  });
});

describe("createPortProblem", () => {
  it("accepts blank (8080 default) and real ports", () => {
    expect(createPortProblem("")).toBeUndefined();
    expect(createPortProblem(" 3000 ")).toBeUndefined();
    expect(createPortProblem("65535")).toBeUndefined();
  });
  it("rejects anything typed that isn't a port", () => {
    for (const bad of ["0", "65536", "abc", "80.5", "-1", "1e3"]) expect(createPortProblem(bad)).toBeTruthy();
  });
});
