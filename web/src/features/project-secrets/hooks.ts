"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  listSharedSecrets,
  setSharedSecret,
  unsetSharedSecret,
  sharedSecretsQueryKey,
  type SharedSecretsList,
} from "./api";

export function useSharedSecrets(project: string) {
  return useQuery<SharedSecretsList>({
    queryKey: sharedSecretsQueryKey(project),
    queryFn: () => listSharedSecrets(project),
    enabled: !!project,
  });
}

export function useSetSharedSecret(project: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { key: string; value: string }) => setSharedSecret(project, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sharedSecretsQueryKey(project) });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });
}

export function useUnsetSharedSecret(project: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) => unsetSharedSecret(project, key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sharedSecretsQueryKey(project) });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
}
