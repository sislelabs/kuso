"use client";

import { useQuery } from "@tanstack/react-query";
import {
  getProject,
  listAddons,
  listEnvironments,
  listProjects,
  listServices,
} from "./api";

export const projectsQueryKey = ["projects"] as const;
export const projectQueryKey = (name: string) => ["projects", name] as const;
export const servicesQueryKey = (project: string) =>
  ["projects", project, "services"] as const;
export const envsQueryKey = (project: string) =>
  ["projects", project, "envs"] as const;
export const addonsQueryKey = (project: string) =>
  ["projects", project, "addons"] as const;

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
  });
}

export function useAddons(project: string) {
  return useQuery({
    queryKey: addonsQueryKey(project),
    queryFn: () => listAddons(project),
    enabled: !!project,
  });
}
