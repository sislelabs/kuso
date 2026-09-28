"use client";

import { useQuery } from "@tanstack/react-query";
import { getIngressTargets } from "./api";

export const ingressTargetsQueryKey = ["config", "ingress"] as const;

// The ingress address changes rarely (new LB, node added), so cache it
// for the session and skip retries on failure.
export function useIngressTargets(enabled = true) {
  return useQuery({
    queryKey: ingressTargetsQueryKey,
    queryFn: getIngressTargets,
    enabled,
    staleTime: 5 * 60_000,
    retry: false,
  });
}
