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
  it("counts a digest once and points at the worst image", () => {
    const s = summarizeVulns([
      img({ project: "a", service: "web", env: "production", digest: "d1", c: [1, 5] }),
      img({ project: "a", service: "web", env: "staging", digest: "d1", c: [1, 5] }),
      img({ project: "b", service: "api", digest: "d2", c: [2, 0] }),
      img({ project: "c", service: "x", digest: "d3", c: [9, 9], status: "failed" }),
    ]);
    expect(s).toMatchObject({ critical: 3, high: 5, fixable: 3, scanned: 2 });
    expect(s.worst?.label).toBe("b / api");
    expect(s.worst?.href).toBe("/projects/b?service=api&tab=vulnerabilities");
  });
});
