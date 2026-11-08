import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api-client";
import { classifyRepoReadFailure, type RepoReadFailureInput } from "./repoReadFailure";

const app = (over: Partial<RepoReadFailureInput>): RepoReadFailureInput => ({
  via: "app",
  repo: "acme/api",
  branch: "main",
  hasToken: false,
  ...over,
});
const url = (over: Partial<RepoReadFailureInput>): RepoReadFailureInput => ({ ...app(over), via: "url", ...over });
const internal = new ApiError(500, { error: "internal" }, "500 Internal Server Error");

describe("classifyRepoReadFailure", () => {
  it.each([new TypeError("Failed to fetch"), new TypeError("Load failed"), new Error("NetworkError when attempting to fetch resource.")])(
    "network failure: %s",
    (error) => {
      const f = classifyRepoReadFailure(app({ error }));
      expect(f.kind).toBe("network");
      expect(f.status).toBeUndefined();
      expect(f.retryable).toBe(true);
    },
  );

  it("GitHub App not configured on the server", () => {
    const error = new ApiError(503, { error: "github not configured: an admin can set up the GitHub App under Settings -> GitHub" }, "503");
    const f = classifyRepoReadFailure(app({ error }));
    expect(f).toMatchObject({ kind: "app-not-configured", status: 503, action: "setup-github" });
  });

  it("App not installed on the owner", () => {
    const f = classifyRepoReadFailure(
      app({ error: internal, access: { ok: false, error: "no GitHub App installation found for owner acme — install the kuso App on this account" } }),
    );
    expect(f).toMatchObject({ kind: "app-not-installed", action: "grant-access", status: 500 });
    expect(f.title).toContain("acme");
  });

  it("App installed but repo not granted", () => {
    const f = classifyRepoReadFailure(app({ error: internal, access: { ok: false, error: "github: repo not found or app has no access" } }));
    expect(f).toMatchObject({ kind: "app-no-access", action: "grant-access" });
    expect(f.title).toContain("acme/api");
  });

  it("App installation forbidden / suspended", () => {
    const f = classifyRepoReadFailure(
      app({ error: internal, access: { ok: false, error: "github: app installation forbidden from accessing repo" } }),
    );
    expect(f).toMatchObject({ kind: "app-forbidden", action: "grant-access" });
  });

  it("App can see the repo but the branch read failed", () => {
    const f = classifyRepoReadFailure(app({ error: internal, branch: "old", access: { ok: true } }));
    expect(f.kind).toBe("branch-unreadable");
    expect(f.title).toContain("old");
  });

  it("App path with no access answer falls back to the HTTP status", () => {
    const f = classifyRepoReadFailure(app({ error: internal }));
    expect(f).toMatchObject({ kind: "server", status: 500, title: "Runtime detection failed (HTTP 500)" });
    expect(f.detail).toMatch(/logs/);
  });

  it("rate limit by status or message", () => {
    expect(classifyRepoReadFailure(app({ error: new ApiError(429, "slow down", "429") })).kind).toBe("rate-limit");
    expect(classifyRepoReadFailure(app({ error: internal, access: { ok: false, error: "GitHub API rate limit exceeded" } })).kind).toBe(
      "rate-limit",
    );
    const note = classifyRepoReadFailure(
      url({ note: "couldn't detect the runtime (GitHub API rate limit reached (HTTP 403)); pick it yourself" }),
    );
    expect(note).toMatchObject({ kind: "rate-limit", action: "use-token" });
  });

  it("pasted URL: 404 without a token is not-found (typo or private)", () => {
    const error = new ApiError(404, { error: "repository not found. If it is private, add an access token." }, "404");
    const f = classifyRepoReadFailure(url({ error }));
    expect(f).toMatchObject({ kind: "not-found", status: 404, action: "use-token" });
    expect(f.detail).toMatch(/typo/);
    expect(f.detail).toMatch(/private/);
  });

  it("pasted URL: 404 with a token means the token can't read it", () => {
    const error = new ApiError(404, { error: "repository not found, or the access token can't read it." }, "404");
    expect(classifyRepoReadFailure(url({ error, hasToken: true })).kind).toBe("token-rejected");
    const viaNote = classifyRepoReadFailure(
      url({ hasToken: true, note: "couldn't detect the runtime (repository not found or not accessible (HTTP 404)); pick it yourself" }),
    );
    expect(viaNote.kind).toBe("token-rejected");
  });

  it("pasted URL: readable repo but missing root directory", () => {
    const f = classifyRepoReadFailure(
      url({ path: "/apps/nope/", note: "couldn't detect the runtime (repository not found or not accessible (HTTP 404)); pick it yourself" }),
    );
    expect(f).toMatchObject({ kind: "path-not-found", title: "No folder apps/nope/ on branch main" });
    expect(classifyRepoReadFailure(url({ path: "README.md", note: "couldn't detect the runtime (README.md is not a directory in acme/api); pick it yourself" })).kind).toBe(
      "path-not-found",
    );
  });

  it("pasted URL: git host unreachable", () => {
    const error = new ApiError(502, { error: "could not reach the repository's git host" }, "502");
    expect(classifyRepoReadFailure(url({ error }))).toMatchObject({ kind: "host-unreachable", status: 502 });
    expect(classifyRepoReadFailure(url({ note: "couldn't detect the runtime (reach GitHub: dial tcp: timeout); pick it yourself" })).kind).toBe(
      "host-unreachable",
    );
  });

  it("pasted URL: invalid URL is not retryable", () => {
    const error = new ApiError(400, { error: "repo URL must start with https://" }, "400");
    expect(classifyRepoReadFailure(url({ error }))).toMatchObject({ kind: "invalid-url", retryable: false, detail: "Repo URL must start with https://" });
  });

  it("non-GitHub host can't be detected and retry won't help", () => {
    const f = classifyRepoReadFailure(url({ note: "runtime detection only works for github.com repos; pick the runtime yourself" }));
    expect(f).toMatchObject({ kind: "unsupported-host", retryable: false });
  });

  it("empty repo", () => {
    expect(classifyRepoReadFailure(url({ note: "the repository has no branches yet" })).kind).toBe("empty-repo");
  });

  it("unknown note passes through", () => {
    const f = classifyRepoReadFailure(url({ note: "couldn't detect the runtime (GitHub API: HTTP 500); pick it yourself" }));
    expect(f.kind).toBe("server");
    expect(f.detail).toContain("HTTP 500");
  });

  it("other API errors keep the server message and status", () => {
    const error = new ApiError(403, { error: "forbidden: missing permission projects:create" }, "403");
    expect(classifyRepoReadFailure(app({ error }))).toMatchObject({
      kind: "server",
      status: 403,
      title: "Runtime detection failed (HTTP 403)",
      detail: "Forbidden: missing permission projects:create",
    });
  });
});
