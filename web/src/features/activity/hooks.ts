"use client";

import { useQuery } from "@tanstack/react-query";
import { listAudit } from "./api";

export const auditQueryKey = (limit: number) => ["audit", limit] as const;

export function useAudit(limit = 100) {
  return useQuery({
    queryKey: auditQueryKey(limit),
    queryFn: () => listAudit(limit),
    refetchInterval: 30_000,
  });
}
