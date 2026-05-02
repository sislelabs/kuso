"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getService,
  getServiceEnv,
  getServiceLogs,
  listBuilds,
  listAddonSecretKeys,
  setServiceEnv,
  triggerBuild,
  wakeService,
} from "./api";
import type { KusoEnvVar } from "@/types/projects";

export const serviceQueryKey = (project: string, service: string) =>
  ["projects", project, "services", service] as const;
export const serviceEnvQueryKey = (project: string, service: string) =>
  ["projects", project, "services", service, "env"] as const;
export const buildsQueryKey = (project: string, service: string) =>
  ["projects", project, "services", service, "builds"] as const;
export const logsTailQueryKey = (project: string, service: string, env: string) =>
  ["projects", project, "services", service, "logs", env] as const;

export function useService(project: string, service: string) {
  return useQuery({
    queryKey: serviceQueryKey(project, service),
    queryFn: () => getService(project, service),
    enabled: !!project && !!service,
  });
}

export function useServiceEnv(project: string, service: string) {
  return useQuery({
    queryKey: serviceEnvQueryKey(project, service),
    queryFn: () => getServiceEnv(project, service),
    enabled: !!project && !!service,
  });
}

export function useSetServiceEnv(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (envVars: KusoEnvVar[]) => setServiceEnv(project, service, envVars),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: serviceEnvQueryKey(project, service) });
    },
  });
}

export function useBuilds(project: string, service: string) {
  return useQuery({
    queryKey: buildsQueryKey(project, service),
    queryFn: () => listBuilds(project, service),
    enabled: !!project && !!service,
    refetchInterval: 10_000,
  });
}

export function useTriggerBuild(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { branch?: string; ref?: string } = {}) =>
      triggerBuild(project, service, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
    },
  });
}

export function useLogsTail(project: string, service: string, env = "production") {
  return useQuery({
    queryKey: logsTailQueryKey(project, service, env),
    queryFn: () => getServiceLogs(project, service, env),
    enabled: !!project && !!service,
  });
}

export function useWakeService(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => wakeService(project, service),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
    },
  });
}

export function useAddonSecretKeys(project: string, addon: string) {
  return useQuery({
    queryKey: ["projects", project, "addons", addon, "secret-keys"] as const,
    queryFn: () => listAddonSecretKeys(project, addon),
    enabled: !!project && !!addon,
    staleTime: 5 * 60_000,
  });
}
