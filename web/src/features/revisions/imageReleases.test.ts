import { describe, expect, it } from "vitest";
import type { Revision } from "./api";
import { imageReleases, shortDigest } from "./imageReleases";

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

describe("shortDigest", () => {
  it("extracts a short sha", () => {
    expect(shortDigest("docker.io/library/nginx@sha256:0123456789abcdef0123")).toBe("sha256:0123456789ab");
    expect(shortDigest("")).toBe("");
  });
});
