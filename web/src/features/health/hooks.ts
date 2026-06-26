"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getReconcileHealth, remediate, type RemediateRequest } from "./api";

export const reconcileHealthQueryKey = ["health", "reconcile"] as const;

// useReconcileHealth polls the reconcile-health scan. 30s cadence —
// helm releases settle on the operator's 3m reconcile loop, so the
// dashboard doesn't need a tight refetch; 30s keeps it fresh enough
// that a remediation the operator picks up shows green within a tick
// or two of the next reconcile.
export function useReconcileHealth() {
  return useQuery({
    queryKey: reconcileHealthQueryKey,
    queryFn: getReconcileHealth,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  });
}

export function useRemediate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: RemediateRequest) => remediate(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: reconcileHealthQueryKey });
    },
  });
}
