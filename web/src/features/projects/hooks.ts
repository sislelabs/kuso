"use client";

import { useQuery, type QueryClient } from "@tanstack/react-query";
import {
  getProject,
  getProjectsSummary,
  listAddons,
  listEnvironments,
  listEnvGroups,
  listProjects,
  listServices,
} from "./api";

export const projectsQueryKey = ["projects"] as const;
export const projectsSummaryQueryKey = ["projects", "summary"] as const;
export const projectQueryKey = (name: string) => ["projects", name] as const;
export const servicesQueryKey = (project: string) =>
  ["projects", project, "services"] as const;
export const envsQueryKey = (project: string) =>
  ["projects", project, "envs"] as const;
export const envGroupsQueryKey = (project: string) =>
  ["projects", project, "env-groups"] as const;
export const addonsQueryKey = (project: string) =>
  ["projects", project, "addons"] as const;

// useEnvGroups reads the project-level environment groupings —
// "production", "staging", "client-demo", plus any preview-pr-N envs.
// Each group spans every cloned service + (per-policy) addon. Used by
// the env switcher in TopNav.
export function useEnvGroups(project: string) {
  return useQuery({
    queryKey: envGroupsQueryKey(project),
    queryFn: () => listEnvGroups(project),
    enabled: !!project,
    // Refetch on the same cadence as the env switcher's parent — fast
    // enough that creating a new env shows up in the dropdown within
    // one paint, slow enough that idle dashboards aren't burning
    // cycles. The list is small (one row per env-group); cost is
    // negligible.
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });
}

export function useProjects() {
  return useQuery({ queryKey: projectsQueryKey, queryFn: listProjects });
}

// useProjectsSummary drives the projects dashboard with ONE request:
// describe rollup + metrics for every accessible project, replacing the
// old per-card describe + per-card metrics useQueries fan-out (2N
// requests for N projects, every poll cycle, per open tab). Polls every
// 30s — metrics-server emits new samples every 15s so 30s gives
// near-fresh data without hammering the API — and only while the tab is
// visible (refetchIntervalInBackground: false), matching the polling
// discipline of the queries it replaces.
export function useProjectsSummary() {
  return useQuery({
    queryKey: projectsSummaryQueryKey,
    queryFn: getProjectsSummary,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    staleTime: 25_000,
  });
}

// The describe payload is what the canvas paints service tiles from
// (replicas, URL, unified env state). Poll it fast while any env is
// mid-transition so a restart/wake/deploy/crashloop shows up without a
// window refocus, slow otherwise.
const TRANSITIONAL_ENV_STATES = new Set(["building", "deploying", "crashlooping", "degraded"]);

export function projectRefetchInterval(
  data: { environments?: { status?: { state?: string } }[] } | undefined,
): number {
  const busy = (data?.environments ?? []).some((e) => TRANSITIONAL_ENV_STATES.has(e.status?.state ?? ""));
  return busy ? 5_000 : 30_000;
}

export function useProject(name: string) {
  return useQuery({
    queryKey: projectQueryKey(name),
    queryFn: () => getProject(name),
    enabled: !!name,
    refetchInterval: (q) => projectRefetchInterval(q.state.data),
    refetchIntervalInBackground: false,
  });
}

// invalidateProjectDescribe refreshes the canvas source of truth
// (["projects", p] exactly). Mutations that only invalidated
// ["projects", p, "envs"] left the canvas frozen, because that key is a
// sibling of the describe query, not a parent.
export function invalidateProjectDescribe(qc: QueryClient, project: string) {
  return qc.invalidateQueries({ queryKey: projectQueryKey(project), exact: true });
}

export function useServices(project: string) {
  return useQuery({
    queryKey: servicesQueryKey(project),
    queryFn: () => listServices(project),
    enabled: !!project,
  });
}

export function useEnvironments(project: string) {
  return useQuery({
    queryKey: envsQueryKey(project),
    queryFn: () => listEnvironments(project),
    enabled: !!project,
    // Poll on the same cadence as useBuilds so the deployments tab's
    // ACTIVE/SUPERSEDED badges flip the moment the build poller
    // promotes a new image tag onto the env CR. Without this, a
    // newly-succeeded build sat as SUPERSEDED in the UI and the
    // older one kept its ACTIVE badge until the user hard-refreshed.
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });
}

export function useAddons(project: string) {
  return useQuery({
    queryKey: addonsQueryKey(project),
    queryFn: () => listAddons(project),
    enabled: !!project,
  });
}
