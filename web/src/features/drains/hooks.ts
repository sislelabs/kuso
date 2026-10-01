"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createDrain, deleteDrain, listDrains, testDrain, type DrainInput } from "./api";

export const drainsQueryKey = ["drains"] as const;

export function useDrains(enabled: boolean) {
  return useQuery({ queryKey: drainsQueryKey, queryFn: listDrains, enabled });
}

export function useCreateDrain() {
  const qc = useQueryClient();
  return useMutation({
    // Callers pass a per-call onError; skip the global toast so failures toast once.
    meta: { skipGlobalErrorToast: true },
    mutationFn: (input: DrainInput) => createDrain(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: drainsQueryKey }),
  });
}

export function useDeleteDrain() {
  const qc = useQueryClient();
  return useMutation({
    // Callers pass a per-call onError; skip the global toast so failures toast once.
    meta: { skipGlobalErrorToast: true },
    mutationFn: (id: string) => deleteDrain(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: drainsQueryKey }),
  });
}

export function useTestDrain() {
  return useMutation({ meta: { skipGlobalErrorToast: true }, mutationFn: (id: string) => testDrain(id) });
}
