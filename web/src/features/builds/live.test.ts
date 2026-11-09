import { describe, expect, it } from "vitest";
import {
  buildNeverReady,
  previousDeployTarget,
  buildNote,
  buildStall,
  buildRefLabel,
  classifyBuild,
  isBranchMismatch,
  isRolledBack,
  liveBuildId,
  waitingLabel,
  previousSucceeded,
  rollForwardTarget,
  newerReleaseFailure,
  servingBuildId,
} from "./live";
import type { DeployBuild } from "./types";

const b = (id: string, over: Partial<DeployBuild> = {}): DeployBuild => ({
  id,
  serviceName: "web",
  status: "succeeded",
  branch: "main",
  ...over,
});

describe("servingBuildId", () => {
  const builds = [b("new", { imageTag: "t-new", liveEnvs: ["production"] }), b("old", { imageTag: "t-old" })];
  it("keeps the previous build live until the promoted one's pods are ready", () => {
    expect(servingBuildId(builds, "new", "t-old")).toBe("old");
  });
  it("is the promoted build once nothing older serves", () => {
    expect(servingBuildId(builds, "new", undefined)).toBe("new");
    expect(servingBuildId(builds, "new", "t-new")).toBe("new");
    expect(servingBuildId(builds, "new", "t-unknown")).toBe("new");
  });
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

describe("buildRefLabel", () => {
  it("cuts a real commit SHA", () => {
    expect(buildRefLabel({ commitSha: "a4b2f1c9e8d7", ref: "a4b2f1c9e8d7" }, 7)).toBe("a4b2f1c");
  });

  it("shows a synthetic ref whole when there is no SHA", () => {
    expect(buildRefLabel({ ref: "main-x7k2p9q" }, 7)).toBe("main-x7k2p9q");
  });

  it("is empty when the server sent neither", () => {
    expect(buildRefLabel({}, 7)).toBe("");
  });
});

describe("previousSucceeded / rollForwardTarget", () => {
  const builds = [
    b("n", { commitSha: "ccccccc", finishedAt: "2026-09-03T00:00:00Z", imageTag: "c" }),
    b("f", { status: "failed", commitSha: "fffffff", finishedAt: "2026-09-02T12:00:00Z" }),
    b("o", { commitSha: "bbbbbbb", finishedAt: "2026-09-02T00:00:00Z", imageTag: "b" }),
    b("p", { branch: "feat", commitSha: "ddddddd", finishedAt: "2026-09-01T12:00:00Z" }),
    b("oo", { commitSha: "aaaaaaa", finishedAt: "2026-09-01T00:00:00Z", imageTag: "a" }),
  ];
  it("finds the older success on the same branch", () => {
    expect(previousSucceeded(builds, builds[0])?.id).toBe("o");
    expect(previousSucceeded(builds, builds[2])?.id).toBe("oo");
    expect(previousSucceeded(builds, builds[4])).toBeUndefined();
  });
  it("rolls forward to the newest newer success only when rolled back", () => {
    expect(rollForwardTarget(builds, builds[2])?.id).toBe("n");
    expect(rollForwardTarget(builds, builds[0])).toBeUndefined();
  });
});

describe("newerReleaseFailure", () => {
  const at = (h: number) => new Date(Date.UTC(2026, 0, 1, h)).toISOString();
  it("returns the newer build whose release hook failed while live stays older", () => {
    const live = b("old", { finishedAt: at(1) });
    const rf = b("new", { status: "release-failed", finishedAt: at(3) });
    expect(newerReleaseFailure([rf, live], live)?.id).toBe("new");
  });
  it("ignores it once a newer attempt succeeded or failed", () => {
    const live = b("old", { finishedAt: at(1) });
    const rf = b("rf", { status: "release-failed", finishedAt: at(2) });
    expect(newerReleaseFailure([b("ok", { finishedAt: at(3) }), rf, live], live)).toBeUndefined();
    expect(newerReleaseFailure([b("bad", { status: "failed", finishedAt: at(3) }), rf, live], live)).toBeUndefined();
  });
  it("skips in-flight builds and other branches, and older release failures", () => {
    const live = b("live", { finishedAt: at(2) });
    const rf = b("rf", { status: "release-failed", finishedAt: at(3) });
    const running = b("run", { status: "running", startedAt: at(4) });
    const other = b("pr", { branch: "feat", status: "succeeded", finishedAt: at(5) });
    expect(newerReleaseFailure([other, running, rf, live], live)?.id).toBe("rf");
    const olderRf = b("orf", { status: "release-failed", finishedAt: at(1) });
    expect(newerReleaseFailure([live, olderRf], live)).toBeUndefined();
  });
});

describe("buildStall", () => {
  const stalled = { stalled: { kind: "image_pull", summary: "Builder image can't be pulled", detail: "ErrImagePull: denied", since: "2026-01-01T00:00:00Z" } };
  it("explains an in-flight build whose pod can't start", () => {
    expect(buildStall(stalled, "running")).toEqual({ summary: "Builder image can't be pulled", detail: "ErrImagePull: denied" });
    expect(buildStall(stalled, "pending")?.summary).toBe("Builder image can't be pulled");
  });
  it("says nothing once the build is settled or not stalled", () => {
    expect(buildStall(stalled, "failed")).toBeUndefined();
    expect(buildStall({}, "running")).toBeUndefined();
  });
});

describe("buildNeverReady", () => {
  const repo = "registry/demo/api";
  const live = { since: Date.parse("2026-11-01T00:00:00Z"), refs: new Set([`${repo}:good`]) };
  const b = (id: string, tag: string, finishedAt: string): DeployBuild => ({ id, serviceName: "api", status: "succeeded", imageTag: tag, finishedAt });
  it("flags a build that rolled out after recording began but never became ready", () => {
    expect(buildNeverReady(b("bad", "bad", "2026-11-02T00:00:00Z"), live, repo, "cur")).toBe(true);
    expect(buildNeverReady(b("good", "good", "2026-11-02T00:00:00Z"), live, repo, "cur")).toBe(false);
  });
  it("treats builds from before the record, the live build and unknowns as fine", () => {
    expect(buildNeverReady(b("old", "old", "2026-10-01T00:00:00Z"), live, repo, "cur")).toBe(false);
    expect(buildNeverReady(b("cur", "cur", "2026-11-02T00:00:00Z"), live, repo, "cur")).toBe(false);
    expect(buildNeverReady(b("bad", "bad", "2026-11-02T00:00:00Z"), null, repo, "cur")).toBe(false);
  });
  it("is skipped as the previous deploy target", () => {
    const builds: DeployBuild[] = [
      { ...b("cur", "cur", "2026-11-03T00:00:00Z"), liveEnvs: ["production"], branch: "main", commitSha: "c3" },
      { ...b("bad", "bad", "2026-11-02T00:00:00Z"), liveEnvs: [], branch: "main", commitSha: "c2" },
      { ...b("good", "good", "2026-11-01T12:00:00Z"), liveEnvs: [], branch: "main", commitSha: "c1" },
    ];
    expect(previousDeployTarget(builds, "production")?.target.id).toBe("bad");
    expect(previousDeployTarget(builds, "production", (x) => buildNeverReady(x, live, repo, "cur"))?.target.id).toBe("good");
  });
});
