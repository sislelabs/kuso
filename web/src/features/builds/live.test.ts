import { describe, expect, it } from "vitest";
import {
  buildNote,
  classifyBuild,
  isBranchMismatch,
  isRolledBack,
  liveBuildId,
  waitingLabel,
} from "./live";
import type { DeployBuild } from "./types";

const b = (id: string, over: Partial<DeployBuild> = {}): DeployBuild => ({
  id,
  serviceName: "web",
  status: "succeeded",
  branch: "main",
  ...over,
});

describe("liveBuildId", () => {
  it("uses liveEnvs when the server sends them, ignoring newer successes", () => {
    const builds = [b("new", { liveEnvs: [] }), b("old", { liveEnvs: ["production"] })];
    expect(liveBuildId(builds, "production", "tag-of-new")).toBe("old");
    expect(liveBuildId(builds, "staging")).toBeUndefined();
  });

  it("falls back to image-tag matching on an old server", () => {
    const builds = [b("new", { imageTag: "t2" }), b("old", { imageTag: "t1" })];
    expect(liveBuildId(builds, "production", "t1")).toBe("old");
  });

  it("falls back to the newest success when the env has no tag", () => {
    const builds = [b("f", { status: "failed" }), b("new"), b("old")];
    expect(liveBuildId(builds, "production")).toBe("new");
  });
});

describe("classifyBuild", () => {
  it("marks only the live build active", () => {
    expect(classifyBuild(b("a"), "a")).toBe("active");
    expect(classifyBuild(b("b"), "a")).toBe("superseded");
    expect(classifyBuild(b("c", { status: "release-failed" }), "c")).toBe("release-failed");
    expect(classifyBuild(b("d", { status: "QUEUED" }), "a")).toBe("queued");
  });
});

describe("isRolledBack", () => {
  it("is true when a newer success exists on the same branch", () => {
    const live = b("old", { finishedAt: "2026-09-01T00:00:00Z" });
    const builds = [b("new", { finishedAt: "2026-09-02T00:00:00Z" }), live];
    expect(isRolledBack(builds, live)).toBe(true);
  });

  it("is false when the live build is the newest success", () => {
    const live = b("new", { finishedAt: "2026-09-02T00:00:00Z" });
    const builds = [live, b("old", { finishedAt: "2026-09-01T00:00:00Z" })];
    expect(isRolledBack(builds, live)).toBe(false);
  });

  it("ignores newer successes on other branches and newer failures", () => {
    const live = b("old", { finishedAt: "2026-09-01T00:00:00Z" });
    const builds = [
      b("pr", { branch: "feat", finishedAt: "2026-09-03T00:00:00Z" }),
      b("bad", { status: "failed", finishedAt: "2026-09-02T00:00:00Z" }),
      live,
    ];
    expect(isRolledBack(builds, live)).toBe(false);
  });

  it("uses list order when timestamps are missing", () => {
    const live = b("old");
    expect(isRolledBack([b("new"), live], live)).toBe(true);
    expect(isRolledBack([live, b("older")], live)).toBe(false);
  });
});

describe("buildNote", () => {
  it("explains a queued build waiting on CI", () => {
    expect(buildNote(b("q", { status: "queued", waitingFor: "ci" }), "queued")).toBe(
      "Waiting for GitHub CI checks",
    );
  });

  it("shows the cancel and not-promoted reasons", () => {
    expect(buildNote(b("c", { cancelReason: "superseded by abc" }), "cancelled")).toBe(
      "superseded by abc",
    );
    expect(buildNote(b("s", { notPromotedReason: "branch moved" }), "superseded")).toBe(
      "branch moved",
    );
    expect(buildNote(b("s"), "superseded")).toBe("");
  });
});

describe("waitingLabel", () => {
  it("passes human text through and wraps bare nouns", () => {
    expect(waitingLabel("waiting for a build slot")).toBe("Waiting for a build slot");
    expect(waitingLabel("a build slot")).toBe("Waiting for a build slot");
  });
});

describe("isBranchMismatch", () => {
  const apiErr = (status: number, message: string) =>
    Object.assign(new Error(message), { status });

  it("matches only a 400 that mentions the branch", () => {
    expect(isBranchMismatch(apiErr(400, "build branch feat does not match env branch main"))).toBe(
      true,
    );
    expect(isBranchMismatch(apiErr(400, "build has not succeeded"))).toBe(false);
    expect(isBranchMismatch(apiErr(500, "branch lookup failed"))).toBe(false);
    expect(isBranchMismatch("branch")).toBe(false);
  });
});
