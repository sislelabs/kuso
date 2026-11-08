import { describe, expect, it } from "vitest";
import type { Revision } from "./api";
import { describeRevision, envRevisionName, summaryItems } from "./describe";
import { groupRevisionRuns, interleaveTimeline } from "./timeline";

const rev = (over: Partial<Revision>): Revision => ({
  id: "r",
  project: "p",
  kind: "service",
  name: "web",
  snapshot: {},
  createdAt: "2026-09-01T00:00:00Z",
  ...over,
});

describe("summaryItems", () => {
  it("collects names across set/unset halves and strips env + secret markers", () => {
    expect(summaryItems("env staging: env set A, B; env unset C (secret)")).toEqual(["A", "B", "C"]);
    expect(summaryItems("domain add a.com")).toEqual(["a.com"]);
  });

  it("returns null for free-form summaries", () => {
    expect(summaryItems("patch")).toBeNull();
    expect(summaryItems("env staging: branch main → dev")).toBeNull();
  });
});

describe("describeRevision", () => {
  it("counts env keys", () => {
    const v = describeRevision(
      rev({ summary: "env set A, B; env unset C", snapshot: { op: "service.envVars", envVars: [] } }),
    );
    expect(v).toEqual({ label: "Env vars changed", detail: "3 keys", revertable: true });
  });

  it("marks informational and unparseable snapshots non-revertable", () => {
    expect(
      describeRevision(
        rev({ summary: "env set K (secret)", snapshot: { op: "service.envSecret", informational: true } }),
      ).revertable,
    ).toBe(false);
    expect(describeRevision(rev({ summary: "patch", snapshot: "garbage" })).revertable).toBe(false);
  });

  it("labels legacy patch rows as settings changes with no detail", () => {
    expect(describeRevision(rev({ summary: "patch", snapshot: { patch: {} } }))).toEqual({
      label: "Settings changed",
      detail: "",
      revertable: true,
    });
  });

  it("uses domain nouns and keeps free-form detail", () => {
    expect(
      describeRevision(rev({ summary: "domain add a.com, b.com", snapshot: { op: "service.domains" } })).detail,
    ).toBe("2 domains");
    expect(
      describeRevision(
        rev({ summary: "env staging: branch main → dev", snapshot: { op: "environment.branch" } }),
      ).detail,
    ).toBe("branch main → dev");
  });
});

describe("envRevisionName", () => {
  it("strips the project prefix", () => {
    expect(envRevisionName("shop", "shop-web-staging")).toBe("web-staging");
    expect(envRevisionName("shop", "other")).toBe("other");
  });
});

describe("interleaveTimeline", () => {
  it("slots revisions between builds by time, keeping untimed builds in place", () => {
    const builds = [
      { id: "q" },
      { id: "b2", startedAt: "2026-09-03T00:00:00Z" },
      { id: "b1", startedAt: "2026-09-01T00:00:00Z" },
    ];
    const revs = [
      rev({ id: "old", createdAt: "2026-08-01T00:00:00Z" }),
      rev({ id: "mid", createdAt: "2026-09-02T00:00:00Z" }),
      rev({ id: "new", createdAt: "2026-09-04T00:00:00Z" }),
    ];
    const order = interleaveTimeline(builds, revs).map((i) =>
      i.type === "build" ? i.build.id : i.revision.id,
    );
    expect(order).toEqual(["q", "new", "b2", "mid", "b1", "old"]);
  });
});

describe("groupRevisionRuns", () => {
  const r = (id: string) => ({ type: "revision" as const, revision: rev({ id }) });
  const build = (id: string) => ({ type: "build" as const, build: { id } });
  it("folds runs of three or more and leaves short runs alone", () => {
    const out = groupRevisionRuns([r("1"), r("2"), r("3"), build("a"), r("4"), build("b"), r("5"), r("6")]);
    expect(out.map((o) => o.type)).toEqual(["revisions", "build", "revision", "build", "revision", "revision"]);
    expect(out[0].type === "revisions" && out[0].revisions.map((r) => r.id)).toEqual(["1", "2", "3"]);
  });
});
