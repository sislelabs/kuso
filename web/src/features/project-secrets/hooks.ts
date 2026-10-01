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

// The env editor reads each service's subscribed shared values under
// ["projects", p, "services", svc, "shared-env-keys", ...]; refresh them
// all so a revealed value isn't stale after a save/delete.
function invalidateSharedSecretViews(qc: ReturnType<typeof useQueryClient>, project: string) {
  qc.invalidateQueries({ queryKey: sharedSecretsQueryKey(project) });
  qc.invalidateQueries({
    predicate: (q) =>
      q.queryKey[0] === "projects" && q.queryKey[1] === project && q.queryKey[4] === "shared-env-keys",
  });
}

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
    // The card handles errors itself: a 409 "shadowed" becomes an
    // overwrite-anyway prompt rather than a toast.
    meta: { skipGlobalErrorToast: true },
    mutationFn: (body: { key: string; value: string; force?: boolean }) => setSharedSecret(project, body),
    onSuccess: () => invalidateSharedSecretViews(qc, project),
  });
}

export function useUnsetSharedSecret(project: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) => unsetSharedSecret(project, key),
    onSuccess: () => invalidateSharedSecretViews(qc, project),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
}
