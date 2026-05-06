"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  deleteService,
  getDetectedEnv,
  getDrift,
  getService,
  getServiceEnv,
  getServiceLogs,
  listBuilds,
  listErrors,
  listAddonSecretKeys,
  patchService,
  setServiceEnv,
  triggerBuild,
  wakeService,
  type PatchServiceBody,
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

// useDetectedEnv polls the build-time + crash-time env-var detection
// surface every 30s — runtime crash hints land on flushInterval (1s)
// in the shipper but the UI doesn't need sub-second refresh, the
// banner just has to appear within "minute or so" of a crash for the
// user to make the connection.
// useDrift polls the spec-vs-running drift report. Short refetch
// interval (10s) so a save on the settings panel surfaces the
// rolling-out indicator within one tick — slower would feel like
// the UI ate the save.
export function useDrift(project: string, service: string) {
  return useQuery({
    queryKey: ["projects", project, "services", service, "drift"] as const,
    queryFn: () => getDrift(project, service),
    enabled: !!project && !!service,
    refetchInterval: 10_000,
    staleTime: 5_000,
  });
}

export function useDetectedEnv(project: string, service: string) {
  return useQuery({
    queryKey: ["projects", project, "services", service, "env", "detected"] as const,
    queryFn: () => getDetectedEnv(project, service),
    enabled: !!project && !!service,
    refetchInterval: 30_000,
    staleTime: 15_000,
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

export const errorsQueryKey = (project: string, service: string, since: string) =>
  ["projects", project, "services", service, "errors", since] as const;

export function useErrors(project: string, service: string, since = "24h") {
  return useQuery({
    queryKey: errorsQueryKey(project, service, since),
    queryFn: () => listErrors(project, service, since),
    enabled: !!project && !!service,
    refetchInterval: 30_000,
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

export function usePatchService(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: PatchServiceBody) => patchService(project, service, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: serviceQueryKey(project, service) });
      qc.invalidateQueries({ queryKey: ["projects", project] });
    },
  });
}

export function useDeleteService(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => deleteService(project, service),
    onSuccess: () => {
      // Cascade: project detail (rolls services + envs into one shape)
      // and the bare services list both need a refetch. Easiest: nuke
      // anything keyed under this project.
      qc.invalidateQueries({ queryKey: ["projects", project] });
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
