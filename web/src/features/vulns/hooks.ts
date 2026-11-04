"use client";

import { useEffect, useRef } from "react";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import {
  addVulnIgnore,
  getAddonVulns,
  getPlatformFindings,
  getPlatformVulns,
  getProjectVulns,
  getServiceVulns,
  getVulnScanSettings,
  listVulnIgnores,
  putVulnScanSettings,
  removeVulnIgnore,
  rescanVulns,
  scanAllVulns,
  setServiceVulnPolicy,
} from "./api";
import { isScanActive, shouldPollScan } from "./logic";
import type { AddVulnIgnoreInput, VulnOwner, VulnPolicy, VulnScanSettings, VulnScanSummary } from "./types";

const POLL_MS = 3_000;

// Every key starts with "vulns" so a settings change can invalidate the
// whole feature in one call. Owner keys nest findings + ignores so an
// ignore/policy change refreshes both with one prefix invalidation.
export const vulnKeys = {
  all: ["vulns"] as const,
  project: (project: string) => ["vulns", "project", project] as const,
  owner: (o: VulnOwner) => ["vulns", o.kind, o.project, o.name] as const,
  findings: (o: VulnOwner) =>
    ["vulns", o.kind, o.project, o.name, "findings", o.kind === "service" ? o.env : ""] as const,
  ignores: (o: VulnOwner) => ["vulns", o.kind, o.project, o.name, "ignores"] as const,
  platform: ["vulns", "admin", "platform"] as const,
  platformFindings: (digest: string) => ["vulns", "admin", "platform", "findings", digest] as const,
  settings: ["vulns", "admin", "settings"] as const,
};

function invalidateOwner(qc: QueryClient, o: VulnOwner) {
  void qc.invalidateQueries({ queryKey: vulnKeys.owner(o) });
  // The project rollup feeds the canvas badges.
  void qc.invalidateQueries({ queryKey: vulnKeys.project(o.project) });
}

// When a polled scan settles, the project rollup (canvas badges) is stale.
function useInvalidateOnScanSettled(scan: VulnScanSummary | null | undefined, project: string) {
  const qc = useQueryClient();
  const wasActive = useRef(false);
  const active = isScanActive(scan);
  useEffect(() => {
    if (wasActive.current && !active) {
      void qc.invalidateQueries({ queryKey: vulnKeys.project(project) });
    }
    wasActive.current = active;
  }, [active, project, qc]);
}

// One request per project; the canvas derives per-node badges from it.
export function useProjectVulns(project: string) {
  return useQuery({
    queryKey: vulnKeys.project(project),
    queryFn: () => getProjectVulns(project),
    enabled: !!project,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    staleTime: 30_000,
    // Older servers 404 here; a missing badge is the right degradation.
    retry: false,
  });
}

export function useServiceVulns(project: string, service: string, env: string, awaitingScanId: number | null = null) {
  const owner: VulnOwner = { kind: "service", project, name: service, env };
  const q = useQuery({
    queryKey: vulnKeys.findings(owner),
    queryFn: () => getServiceVulns(project, service, env),
    enabled: !!project && !!service,
    refetchInterval: (query) => (shouldPollScan(query.state.data?.image?.scan, awaitingScanId) ? POLL_MS : false),
  });
  useInvalidateOnScanSettled(q.data?.image?.scan, project);
  return q;
}

export function useAddonVulns(project: string, addon: string, awaitingScanId: number | null = null) {
  const owner: VulnOwner = { kind: "addon", project, name: addon };
  const q = useQuery({
    queryKey: vulnKeys.findings(owner),
    queryFn: () => getAddonVulns(project, addon),
    enabled: !!project && !!addon,
    refetchInterval: (query) => (shouldPollScan(query.state.data?.image?.scan, awaitingScanId) ? POLL_MS : false),
  });
  useInvalidateOnScanSettled(q.data?.image?.scan, project);
  return q;
}

export function useVulnIgnores(owner: VulnOwner) {
  return useQuery({
    queryKey: vulnKeys.ignores(owner),
    queryFn: () => listVulnIgnores(owner),
    enabled: !!owner.project && !!owner.name,
  });
}

export function useRescanVulns(owner: VulnOwner) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => rescanVulns(owner),
    onSuccess: () => invalidateOwner(qc, owner),
  });
}

export function useSetServiceVulnPolicy(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (mode: VulnPolicy) => setServiceVulnPolicy(project, service, mode),
    onSuccess: () => invalidateOwner(qc, { kind: "service", project, name: service, env: "" }),
  });
}

export function useAddVulnIgnore(owner: VulnOwner) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: AddVulnIgnoreInput) => addVulnIgnore(owner, input),
    onSuccess: () => invalidateOwner(qc, owner),
  });
}

export function useRemoveVulnIgnore(owner: VulnOwner) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => removeVulnIgnore(owner, id),
    onSuccess: () => invalidateOwner(qc, owner),
  });
}

export function usePlatformVulns() {
  return useQuery({
    queryKey: vulnKeys.platform,
    queryFn: getPlatformVulns,
    refetchInterval: (q) => ((q.state.data?.images ?? []).some((i) => isScanActive(i.scan)) ? POLL_MS * 2 : false),
  });
}

export function usePlatformFindings(digest: string) {
  return useQuery({
    queryKey: vulnKeys.platformFindings(digest),
    queryFn: () => getPlatformFindings(digest),
    enabled: !!digest,
  });
}

export function useScanAllVulns() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: scanAllVulns,
    onSuccess: () => void qc.invalidateQueries({ queryKey: vulnKeys.all }),
  });
}

export function useVulnScanSettings() {
  return useQuery({ queryKey: vulnKeys.settings, queryFn: getVulnScanSettings });
}

export function useSaveVulnScanSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: VulnScanSettings) => putVulnScanSettings(s),
    onSuccess: (saved) => {
      qc.setQueryData(vulnKeys.settings, saved);
      // Instance mode feeds every service's effective mode.
      void qc.invalidateQueries({ queryKey: vulnKeys.all });
    },
  });
}
