import { describe, expect, it } from "vitest";
import type { Revision } from "@/features/revisions";
import { deployMarkers } from "./deployMarkers";

const rev = (id: string, createdAt: string, snapshot: unknown, extra: Partial<Revision> = {}): Revision => ({
  id,
  project: "shop",
  kind: "service",
  name: "web",
  createdAt,
  snapshot,
  ...extra,
});

const build = (id: string, finishedAt: string, branch = "main", status = "succeeded") => ({
  id,
  status,
  branch,
  commitSha: `${id}aaaaaaaaaaaa`,
  finishedAt,
});

describe("deployMarkers", () => {
  it("marks image deploys, rollbacks and config rollouts, not just builds", () => {
    const ms = deployMarkers({
      builds: [],
      revisions: [
        rev("1", "2026-11-09T10:00:00Z", { patch: { image: { repository: "traefik/whoami", tag: "v1.10" } } }),
        rev("2", "2026-11-09T10:10:00Z", { op: "environment.image", image: { repository: "traefik/whoami", tag: "v1.9" }, revertOf: "1" }),
        rev("3", "2026-11-09T10:20:00Z", { op: "service.envVars", envVars: [] }),
        rev("4", "2026-11-09T10:30:00Z", { op: "service.domains", domains: [] }),
      ],
    });
    expect(ms.map((m) => m.label)).toEqual(["Deploy v1.10", "Rollback to v1.9", "Config rollout · Env vars changed"]);
  });

  it("keeps builds of the env's branch only, and only successful ones", () => {
    const ms = deployMarkers({
      builds: [build("abc", "2026-11-09T10:00:00Z"), build("pr1", "2026-11-09T10:05:00Z", "feat/x"), build("bad", "2026-11-09T10:09:00Z", "main", "failed")],
      revisions: [],
      envBranch: "main",
    });
    expect(ms.map((m) => m.label)).toEqual(["Deploy abcaaaa"]);
  });

  it("always marks the canonical last deploy, once", () => {
    // No build or revision record of it (e.g. a restart-free rollout).
    const alone = deployMarkers({ builds: [], revisions: [], lastDeployAt: "2026-11-09T10:46:00Z" });
    expect(alone).toEqual([{ t: Date.parse("2026-11-09T10:46:00Z"), label: "Last deploy", detail: "When the version serving now went live" }]);
    // Same moment as a build: one marker, the build's.
    const merged = deployMarkers({ builds: [build("abc", "2026-11-09T10:45:30Z")], revisions: [], lastDeployAt: "2026-11-09T10:46:00Z" });
    expect(merged.map((m) => m.label)).toEqual(["Deploy abcaaaa"]);
  });
});
