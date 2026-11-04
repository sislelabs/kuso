import { describe, expect, it } from "vitest";
import { canvasVulnBadges, filterFindings, parseExpiryDays, safeHttpUrl, shouldPollScan, sortFindings } from "./logic";
import type { Severity, VulnFinding, VulnImageReport, VulnScanStatus } from "./types";

const f = (vulnId: string, severity: Severity, extra: Partial<VulnFinding> = {}): VulnFinding => ({
  vulnId,
  severity,
  pkgName: "pkg",
  pkgType: "debian",
  installedVersion: "1.0",
  ignored: false,
  ...extra,
});

describe("sortFindings", () => {
  it("orders by severity, then fixable first, then id", () => {
    const out = sortFindings([
      f("CVE-3", "LOW"),
      f("CVE-2", "CRITICAL"),
      f("CVE-1", "CRITICAL", { fixedVersion: "1.1" }),
      f("CVE-4", "UNKNOWN"),
      f("CVE-0", "HIGH"),
    ]);
    expect(out.map((x) => x.vulnId)).toEqual(["CVE-1", "CVE-2", "CVE-0", "CVE-3", "CVE-4"]);
  });
});

describe("filterFindings", () => {
  const all = [
    f("A", "CRITICAL", { fixedVersion: "2" }),
    f("B", "CRITICAL"),
    f("C", "HIGH", { fixedVersion: "2", ignored: true }),
    f("D", "LOW"),
  ];
  const ids = (xs: VulnFinding[]) => xs.map((x) => x.vulnId);

  it("empty severity set means all", () => {
    expect(ids(filterFindings(all, { severities: new Set(), fixableOnly: false, showIgnored: true }))).toEqual([
      "A",
      "B",
      "C",
      "D",
    ]);
  });

  it("applies severity, fixable and ignored filters together", () => {
    const sev = new Set<Severity>(["CRITICAL", "HIGH"]);
    expect(ids(filterFindings(all, { severities: sev, fixableOnly: true, showIgnored: true }))).toEqual(["A", "C"]);
    expect(ids(filterFindings(all, { severities: sev, fixableOnly: true, showIgnored: false }))).toEqual(["A"]);
  });

  it("treats a blank fixedVersion as not fixable", () => {
    const xs = [f("E", "HIGH", { fixedVersion: " " })];
    expect(filterFindings(xs, { severities: new Set(), fixableOnly: true, showIgnored: true })).toEqual([]);
  });
});

describe("parseExpiryDays", () => {
  it("blank means no expiry", () => {
    expect(parseExpiryDays("  ")).toEqual({ ok: true, days: undefined });
  });
  it("accepts the 1..365 bounds", () => {
    expect(parseExpiryDays("1")).toEqual({ ok: true, days: 1 });
    expect(parseExpiryDays("365")).toEqual({ ok: true, days: 365 });
  });
  it.each(["0", "366", "1.5", "-3", "30d", "abc"])("rejects %s", (raw) => {
    expect(parseExpiryDays(raw).ok).toBe(false);
  });
});

describe("shouldPollScan", () => {
  const scan = (id: number, status: VulnScanStatus) => ({ id, status });
  it("polls while queued or running", () => {
    expect(shouldPollScan(scan(1, "queued"), null)).toBe(true);
    expect(shouldPollScan(scan(1, "running"), null)).toBe(true);
    expect(shouldPollScan(scan(1, "succeeded"), null)).toBe(false);
  });
  it("keeps polling until the requested scan appears, then stops when it settles", () => {
    expect(shouldPollScan(scan(4, "succeeded"), 5)).toBe(true);
    expect(shouldPollScan(null, 5)).toBe(true);
    expect(shouldPollScan(scan(5, "failed"), 5)).toBe(false);
  });
});

describe("safeHttpUrl", () => {
  it("only lets http(s) through", () => {
    expect(safeHttpUrl("https://avd.aquasec.com/nvd/cve-2024-1")).toBe("https://avd.aquasec.com/nvd/cve-2024-1");
    expect(safeHttpUrl("http://example.com/x")).toBe("http://example.com/x");
    expect(safeHttpUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeHttpUrl("data:text/html,hi")).toBeUndefined();
    expect(safeHttpUrl("/relative")).toBeUndefined();
    expect(safeHttpUrl(undefined)).toBeUndefined();
  });
});

describe("canvasVulnBadges", () => {
  const img = (
    over: Partial<VulnImageReport>,
    fixableCritical: number,
    status: VulnScanStatus = "succeeded",
  ): VulnImageReport => ({
    ownerKind: "service",
    imageRef: "r",
    digest: "sha256:x",
    ...over,
    scan: {
      id: 1,
      digest: "sha256:x",
      imageRef: "r",
      status,
      reason: "nightly",
      createdAt: "2026-11-04T00:00:00Z",
      counts: { critical: 0, high: 0, medium: 0, low: 0, unknown: 0, fixableCritical, fixableHigh: 0 },
      ignoredCount: 0,
    },
  });

  it("keys by short name, scopes services to the selected env, takes the max", () => {
    const images = [
      img({ service: "shop-web", env: "production" }, 2),
      img({ service: "web", env: "production" }, 3),
      img({ service: "web", env: "staging" }, 9),
      img({ service: "api" }, 1),
      img({ ownerKind: "addon", addon: "shop-db" }, 4),
      img({ service: "worker", env: "production" }, 0),
    ];
    expect(canvasVulnBadges(images, "shop", "production")).toEqual({
      services: { web: 3, api: 1 },
      addons: { db: 4 },
    });
    expect(canvasVulnBadges(images, "shop", "staging").services).toEqual({ web: 9 });
  });

  it("ignores scans that have not succeeded and platform images", () => {
    const images = [
      img({ service: "web" }, 5, "running"),
      img({ service: "web" }, 5, "failed"),
      img({ ownerKind: "platform", workload: "traefik" }, 7),
    ];
    expect(canvasVulnBadges(images, "shop", "production")).toEqual({ services: {}, addons: {} });
  });
});
