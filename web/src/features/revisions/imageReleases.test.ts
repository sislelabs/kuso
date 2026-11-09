import { describe, expect, it } from "vitest";
import type { Revision } from "./api";
import { collapseFlipFlops, imageReleases, liveImagesOf, neverRan, shortDigest } from "./imageReleases";

const rev = (id: string, createdAt: string, snapshot: unknown): Revision => ({
  id,
  project: "p",
  kind: "service",
  name: "web",
  snapshot,
  createdAt,
});

describe("imageReleases", () => {
  const revs = [
    rev("a", "2026-11-01T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.27" } } }),
    rev("b", "2026-11-03T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.28" } } }),
    rev("c", "2026-11-02T00:00:00Z", { patch: { port: 80 } }),
    rev("d", "2026-11-04T00:00:00Z", { op: "service.envVars", envVars: [] }),
    rev("e", "2026-11-05T00:00:00Z", { patch: { image: { repository: "nginx" } } }),
  ];

  it("splits image changes from other config, newest first, and marks the live one", () => {
    const { releases, other } = imageReleases(revs, { repository: "nginx", tag: "1.28" });
    expect(releases.map((r) => [r.revision.id, r.ref, r.current])).toEqual([
      ["e", "nginx:latest", false],
      ["b", "nginx:1.28", true],
      ["a", "nginx:1.27", false],
    ]);
    expect(other.map((r) => r.id)).toEqual(["d", "c"]);
  });

  it("marks only the newest matching release as current", () => {
    const again = [...revs, rev("f", "2026-11-06T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.27" } } })];
    const { releases } = imageReleases(again, { repository: "nginx", tag: "1.27" });
    expect(releases.filter((r) => r.current).map((r) => r.revision.id)).toEqual(["f"]);
  });
});

describe("release source", () => {
  it("tells a rollback from a hand edit and the create-time image", () => {
    const { releases } = imageReleases(
      [
        rev("r", "2026-11-03T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.27" } }, revertOf: "x" }),
        rev("m", "2026-11-02T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.28" } } }),
        { ...rev("c", "2026-11-01T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.27" } } }), summary: "create" },
      ],
      undefined,
    );
    expect(releases.map((r) => r.source)).toEqual(["rollback", "edit", "create"]);
  });
});

describe("collapseFlipFlops", () => {
  const tags = (ts: string[]) =>
    imageReleases(
      ts.map((t, i) => rev(`r${i}`, new Date(Date.UTC(2026, 10, 1, 0, ts.length - i)).toISOString(), { patch: { image: { repository: "nginx", tag: t } } })),
      { repository: "nginx", tag: ts[0] },
    ).releases;

  it("folds a back-and-forth between two tags into its newest step", () => {
    const out = collapseFlipFlops(tags(["1.28", "1.27", "1.28", "1.27", "1.28", "1.26"]));
    expect(out.map((r) => [r.tag, r.folded?.length ?? 0])).toEqual([
      ["1.28", 4],
      ["1.26", 0],
    ]);
    expect(out[0].current).toBe(true);
  });

  it("leaves a plain upgrade path alone", () => {
    expect(collapseFlipFlops(tags(["1.29", "1.28", "1.27"])).map((r) => r.folded)).toEqual([undefined, undefined, undefined]);
  });

  it("needs three steps to fold", () => {
    expect(collapseFlipFlops(tags(["1.28", "1.27"])).length).toBe(2);
  });
});

describe("shortDigest", () => {
  it("extracts a short sha", () => {
    expect(shortDigest("docker.io/library/nginx@sha256:0123456789abcdef0123")).toBe("sha256:0123456789ab");
    expect(shortDigest("")).toBe("");
  });
});

describe("env-scoped rollbacks and never-ran releases", () => {
  const revs = [
    rev("a", "2026-11-01T00:00:00Z", { patch: { image: { repository: "nginx", tag: "1.27" } } }),
    rev("b", "2026-11-05T00:00:00Z", { patch: { image: { repository: "nginx", tag: "does-not-exist" } } }),
    { ...rev("e", "2026-11-06T00:00:00Z", { op: "environment.image", image: { repository: "nginx", tag: "1.27" }, revertOf: "a" }), kind: "environment" as const },
  ];

  it("lists an env image rollback as the env's current release", () => {
    const { releases, other } = imageReleases(revs, { repository: "nginx", tag: "1.27" });
    expect(releases.map((r) => [r.revision.id, r.current, r.source])).toEqual([
      ["e", true, "rollback"],
      ["b", false, "edit"],
      ["a", false, "edit"],
    ]);
    expect(other).toEqual([]);
  });

  it("marks a release that never fully rolled out, but not ones from before the record", () => {
    const live = liveImagesOf({ "kuso.sislelabs.com/live-images": JSON.stringify({ since: "2026-11-02T00:00:00Z", refs: ["nginx:1.27"] }) });
    const { releases } = imageReleases(revs, { repository: "nginx", tag: "1.27" });
    expect(releases.map((r) => neverRan(r, live))).toEqual([false, true, false]);
    expect(releases.map((r) => neverRan(r, null))).toEqual([false, false, false]);
    expect(liveImagesOf({ "kuso.sislelabs.com/live-images": "{oops" })).toBeNull();
  });
});
