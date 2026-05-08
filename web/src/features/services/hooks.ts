"use client";

import { useEffect, useRef } from "react";
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
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });
}

export function useDetectedEnv(project: string, service: string) {
  return useQuery({
    queryKey: ["projects", project, "services", service, "env", "detected"] as const,
    queryFn: () => getDetectedEnv(project, service),
    enabled: !!project && !!service,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    staleTime: 15_000,
  });
}

export function useSetServiceEnv(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (envVars: KusoEnvVar[]) => setServiceEnv(project, service, envVars),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: serviceEnvQueryKey(project, service) });
      // Drift report compares env CR ↔ live Deployment; the save
      // we just made invalidates that comparison. Force a refetch
      // so the "out of date — restart needed" banner appears
      // within the cycle the user just clicked Save in, not 10s
      // later when the periodic poll fires.
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "drift"] });
    },
  });
}

export function useBuilds(project: string, service: string) {
  const qc = useQueryClient();
  const buildsQ = useQuery({
    queryKey: buildsQueryKey(project, service),
    queryFn: () => listBuilds(project, service),
    enabled: !!project && !!service,
    // 5s while a build is in flight (pending/running) so chip
    // transitions catch up fast; back to 10s when everything is
    // settled. We sniff the latest build's status from the cached
    // data — undefined on first load so the first refetch is at 5s
    // (cheap), and any user opening the panel gets snappy updates
    // through the build lifecycle.
    refetchInterval: (q) => {
      const list = q.state.data ?? [];
      const newest = list[0];
      const s = (newest?.status ?? "").toLowerCase();
      if (s === "pending" || s === "running" || s === "queued") return 3_000;
      return 10_000;
    },
    refetchIntervalInBackground: false,
  });
  // When the latest build flips to "succeeded", invalidate envs +
  // drift right away so ACTIVE/SUPERSEDED chips flip without waiting
  // for the next 10s envs poll. We dedupe by (project, service,
  // newest-id+status) — only fire when *that pair* changes.
  const lastSeenRef = useRef<string>("");
  useEffect(() => {
    const list = buildsQ.data ?? [];
    if (list.length === 0) return;
    const newest = list[0];
    const key = `${newest.id}:${(newest.status ?? "").toLowerCase()}`;
    if (key === lastSeenRef.current) return;
    const prevKey = lastSeenRef.current;
    lastSeenRef.current = key;
    // Only invalidate on a transition INTO succeeded — first mount
    // shouldn't trigger an unnecessary refetch storm.
    if (prevKey && (newest.status ?? "").toLowerCase() === "succeeded") {
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "drift"] });
    }
  }, [buildsQ.data, project, service, qc]);
  return buildsQ;
}

export const errorsQueryKey = (project: string, service: string, since: string) =>
  ["projects", project, "services", service, "errors", since] as const;

export function useErrors(project: string, service: string, since = "24h") {
  return useQuery({
    queryKey: errorsQueryKey(project, service, since),
    queryFn: () => listErrors(project, service, since),
    enabled: !!project && !!service,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
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
