import { describe, expect, it } from "vitest";
import type { VulnImageReport } from "@/features/vulns/types";
import { canvasVulnSummaries, compactCount, errorChipHot } from "./badgeSummary";

const img = (extra: Partial<VulnImageReport>, counts: { fc: number; c: number; h: number }, status = "succeeded"): VulnImageReport => ({
  ownerKind: "service",
  imageRef: "r",
  digest: "d",
  ...extra,
  scan: {
    id: 1,
    digest: "d",
    imageRef: "r",
    status: status as "succeeded",
    reason: "gate",
    createdAt: "",
    ignoredCount: 0,
    counts: { critical: counts.c, high: counts.h, medium: 0, low: 0, unknown: 0, fixableCritical: counts.fc, fixableHigh: 0 },
  },
});

describe("canvasVulnSummaries", () => {
  it("scopes services to the env group, takes the per-field max, skips unfinished scans", () => {
    const out = canvasVulnSummaries(
      [
        img({ service: "p-web" }, { fc: 2, c: 3, h: 9 }),
        img({ service: "p-web", env: "production" }, { fc: 5, c: 1, h: 4 }),
        img({ service: "p-web", env: "staging" }, { fc: 50, c: 50, h: 50 }),
        img({ service: "p-api" }, { fc: 9, c: 9, h: 9 }, "running"),
        img({ ownerKind: "addon", addon: "p-db" }, { fc: 1, c: 2, h: 3 }),
      ],
      "p",
      "production",
    );
    expect(out.services).toEqual({ web: { fixableCritical: 5, critical: 3, high: 9 } });
    expect(out.addons).toEqual({ db: { fixableCritical: 1, critical: 2, high: 3 } });
  });
});

describe("compactCount", () => {
  it("abbreviates thousands", () => {
    expect([compactCount(999), compactCount(1000), compactCount(1250), compactCount(12_400)]).toEqual(["999", "1k", "1.3k", "12k"]);
  });
});

describe("errorChipHot", () => {
  it("keeps a steady trickle neutral and flags new or spiking errors", () => {
    expect(errorChipHot(205, 8)).toBe(false); // 8/h against ~8.5/h average
    expect(errorChipHot(205, 40)).toBe(true); // spike
    expect(errorChipHot(4, 4)).toBe(true); // all new this hour
    expect(errorChipHot(1, 1)).toBe(false); // a single new line is noise
    expect(errorChipHot(30, 0)).toBe(false);
    expect(errorChipHot(60, undefined)).toBe(true); // no last-hour data: day threshold
    expect(errorChipHot(12, undefined)).toBe(false);
  });
});
