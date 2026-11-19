"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { getProjectVulns, vulnKeys, type VulnImageReport } from "@/features/vulns";
import { getProjectUptime, summarizeUptime, type UptimeServiceStatus } from "@/features/uptime";
import { summarizeAddonBackups, type AddonBackupCoverage } from "@/components/health/backupCoverage";

export type PanelKey = "running" | "requests" | "errors" | "capacity" | "deploys" | "cves" | "uptime" | "backups" | "insights";

export interface PanelDef {
  key: PanelKey;
  label: string;
  hint: string;
  adminOnly?: boolean;
}

export const PANELS: readonly PanelDef[] = [
  { key: "running", label: "Running", hint: "Running and stopped projects" },
  { key: "requests", label: "Requests", hint: "Requests in the last 24h" },
  { key: "errors", label: "Errors", hint: "Errors in the last 24h" },
  { key: "capacity", label: "Memory & CPU", hint: "What the servers are using" },
  { key: "deploys", label: "Deploys", hint: "Deploys in the last 24h" },
  { key: "cves", label: "CVEs", hint: "Critical and high CVEs in running images" },
  { key: "uptime", label: "Uptime", hint: "Uptime checks that are passing" },
  { key: "backups", label: "Backups", hint: "Addons with a working scheduled backup", adminOnly: true },
  { key: "insights", label: "Insights link", hint: "Open the compare-services page" },
];

export const DEFAULT_PANELS: readonly PanelKey[] = ["running", "requests", "errors", "capacity", "deploys", "cves", "insights"];

const KEY = "kuso.home.panels";

function parse(raw: string | null): PanelKey[] {
  if (!raw) return [...DEFAULT_PANELS];
  try {
    const v: unknown = JSON.parse(raw);
    if (!Array.isArray(v)) return [...DEFAULT_PANELS];
    return PANELS.map((p) => p.key).filter((k) => v.includes(k));
  } catch {
    return [...DEFAULT_PANELS];
  }
}

// Per-browser panel choice. Read after mount so the static-export
// prerender and the first client render agree.
export function usePanelPrefs(): [PanelKey[], (next: PanelKey[]) => void] {
  const [panels, setPanels] = useState<PanelKey[]>([...DEFAULT_PANELS]);
  useEffect(() => {
    try {
      setPanels(parse(window.localStorage.getItem(KEY)));
    } catch {
      // Storage blocked: defaults are fine.
    }
  }, []);
  const update = useCallback((next: PanelKey[]) => {
    const ordered = PANELS.map((p) => p.key).filter((k) => next.includes(k));
    setPanels(ordered);
    try {
      window.localStorage.setItem(KEY, JSON.stringify(ordered));
    } catch {
      // Not persisted; still applied for this visit.
    }
  }, []);
  return [panels, update];
}

export interface FleetVulns {
  critical: number;
  high: number;
  fixable: number;
  scanned: number;
  /** The image with the most critical, then high, CVEs. */
  worst?: { label: string; href: string; critical: number; high: number };
}

function imageHref(im: VulnImageReport): string | undefined {
  if (!im.project) return undefined;
  const p = encodeURIComponent(im.project);
  if (im.ownerKind === "service" && im.service) return `/projects/${p}?service=${encodeURIComponent(im.service)}&tab=vulnerabilities`;
  if (im.ownerKind === "addon" && im.addon) return `/projects/${p}?addon=${encodeURIComponent(im.addon)}&tab=vulnerabilities`;
  return `/projects/${p}`;
}

// summarizeVulns counts each image digest once: one image running in
// several envs is one set of CVEs to fix, not several.
export function summarizeVulns(images: readonly VulnImageReport[]): FleetVulns {
  const out: FleetVulns = { critical: 0, high: 0, fixable: 0, scanned: 0 };
  const seen = new Set<string>();
  let worstScore = 0;
  for (const im of images) {
    const c = im.scan?.status === "succeeded" ? im.scan.counts : undefined;
    if (!c || seen.has(im.digest)) continue;
    seen.add(im.digest);
    out.scanned++;
    out.critical += c.critical;
    out.high += c.high;
    out.fixable += c.fixableCritical + c.fixableHigh;
    const score = c.critical * 1000 + c.high;
    const href = imageHref(im);
    if (score > worstScore && href) {
      worstScore = score;
      const name = im.service ?? im.addon ?? "";
      out.worst = { label: name && name !== im.project ? `${im.project} / ${name}` : (im.project ?? ""), href, critical: c.critical, high: c.high };
    }
  }
  return out;
}

export function useFleetVulns(projects: readonly string[], enabled: boolean): FleetVulns | undefined {
  const results = useQueries({
    queries: projects.map((p) => ({
      queryKey: vulnKeys.project(p),
      queryFn: () => getProjectVulns(p),
      enabled,
      refetchInterval: 5 * 60_000,
      staleTime: 60_000,
      retry: false,
    })),
  });
  const stamp = results.map((q) => q.dataUpdatedAt).join(",");
  return useMemo(() => {
    if (!enabled || !results.some((q) => q.data)) return undefined;
    return summarizeVulns(results.flatMap((q) => q.data?.images ?? []));
    // results is a new array each render; stamp tracks the data that matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stamp, enabled]);
}

export interface FleetUptime {
  /** false when the checker is off instance-wide */
  enabled: boolean;
  up: number;
  checked: number;
  /** checks that are down or failing */
  bad: { project: string; service: string }[];
}

export function useFleetUptime(projects: readonly string[], enabled: boolean): FleetUptime | undefined {
  const results = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["uptime", p],
      queryFn: () => getProjectUptime(p),
      enabled,
      refetchInterval: 60_000,
      staleTime: 30_000,
      retry: false,
    })),
  });
  const stamp = results.map((q) => q.dataUpdatedAt).join(",");
  return useMemo(() => {
    if (!enabled || !results.some((q) => q.data)) return undefined;
    const all: (UptimeServiceStatus & { project: string })[] = [];
    let on = false;
    results.forEach((q, i) => {
      if (!q.data) return;
      on ||= q.data.enabled;
      for (const s of q.data.services ?? []) all.push({ ...s, project: projects[i] });
    });
    const sum = summarizeUptime(all);
    return {
      enabled: on,
      up: sum.up,
      checked: sum.checked,
      bad: all.filter((s) => s.state === "down" || s.state === "failing").map((s) => ({ project: s.project, service: s.service })),
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stamp, enabled, projects]);
}

interface BackupHealthResp {
  addonBackups: { covered: boolean; healthy: boolean }[];
  backup: { healthy: boolean };
}

export interface FleetBackups extends AddonBackupCoverage {
  controlPlaneHealthy: boolean;
}

// Shares SettingsOverview's query key, so the two make one request.
export function useFleetBackups(enabled: boolean): FleetBackups | undefined {
  const q = useQuery({
    queryKey: ["admin", "backup-health"],
    queryFn: () => api<BackupHealthResp>("/api/admin/backup-health"),
    staleTime: 60_000,
    enabled,
  });
  return useMemo(() => {
    if (!enabled || !q.data) return undefined;
    return { ...summarizeAddonBackups(q.data.addonBackups ?? []), controlPlaneHealthy: q.data.backup?.healthy ?? false };
  }, [enabled, q.data]);
}
