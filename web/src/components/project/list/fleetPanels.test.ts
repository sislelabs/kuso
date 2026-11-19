import { describe, expect, it } from "vitest";
import type { VulnImageReport } from "@/features/vulns";
import { summarizeVulns } from "./fleetPanels";

const counts = (critical: number, high: number) => ({ critical, high, medium: 0, low: 0, unknown: 0, fixableCritical: critical, fixableHigh: 0 });
const img = (o: Partial<VulnImageReport> & { digest: string; c: [number, number]; status?: string }): VulnImageReport =>
  ({
    ownerKind: "service",
    imageRef: "x",
    ...o,
    scan: { status: o.status ?? "succeeded", counts: counts(...o.c) },
  }) as unknown as VulnImageReport;

describe("summarizeVulns", () => {
  it("splits production from staging and addons, counting a digest once", () => {
    const s = summarizeVulns([
      img({ project: "a", service: "web", env: "production", digest: "d1", c: [1, 5] }),
      // Same image in staging: already counted under production.
      img({ project: "a", service: "web", env: "staging", digest: "d1", c: [1, 5] }),
      img({ project: "b", service: "api", env: "production", digest: "d2", c: [2, 0] }),
      img({ project: "b", service: "api", env: "staging", digest: "d4", c: [8, 1] }),
      img({ ownerKind: "addon", project: "b", addon: "db", digest: "d5", c: [3, 2] }),
      img({ project: "c", service: "x", env: "production", digest: "d3", c: [9, 9], status: "failed" }),
    ]);
    expect(s.production).toEqual({ critical: 3, high: 5, images: 2 });
    expect(s.other).toEqual({ critical: 11, high: 3 });
    expect(s.worst?.label).toBe("b / api");
    expect(s.worst?.href).toBe("/projects/b?service=api&tab=vulnerabilities");
  });
});
