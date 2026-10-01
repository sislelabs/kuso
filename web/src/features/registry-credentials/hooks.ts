"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  listRegistryCredentials,
  loginRegistry,
  logoutRegistry,
  registryCredentialsQueryKey,
  type RegistryCredential,
  type RegistryLoginBody,
} from "./api";

export function useRegistryCredentials(project: string) {
  return useQuery<RegistryCredential[]>({
    queryKey: registryCredentialsQueryKey(project),
    queryFn: () => listRegistryCredentials(project),
    enabled: !!project,
  });
}

export function useRegistryLogin(project: string) {
  const qc = useQueryClient();
  return useMutation({
    // Callers pass a per-call onError; skip the global toast so failures toast once.
    meta: { skipGlobalErrorToast: true },
    mutationFn: (body: RegistryLoginBody) => loginRegistry(project, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: registryCredentialsQueryKey(project) }),
  });
}

export function useRegistryLogout(project: string) {
  const qc = useQueryClient();
  return useMutation({
    // Callers pass a per-call onError; skip the global toast so failures toast once.
    meta: { skipGlobalErrorToast: true },
    mutationFn: (registry: string) => logoutRegistry(project, registry),
    onSuccess: () => qc.invalidateQueries({ queryKey: registryCredentialsQueryKey(project) }),
  });
}
