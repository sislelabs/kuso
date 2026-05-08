"use client";

import { useQuery } from "@tanstack/react-query";
import {
  getProject,
  listAddons,
  listEnvironments,
  listEnvGroups,
  listProjects,
  listServices,
} from "./api";

export const projectsQueryKey = ["projects"] as const;
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

export function useProject(name: string) {
  return useQuery({
    queryKey: projectQueryKey(name),
    queryFn: () => getProject(name),
    enabled: !!name,
  });
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
