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
    mutationFn: (input: DrainInput) => createDrain(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: drainsQueryKey }),
  });
}

export function useDeleteDrain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteDrain(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: drainsQueryKey }),
  });
}

export function useTestDrain() {
  return useMutation({ mutationFn: (id: string) => testDrain(id) });
}
