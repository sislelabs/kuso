"use client";

import { useQuery } from "@tanstack/react-query";
import { listKubeEvents } from "./api";

export function useKubeEvents(namespace: string, enabled: boolean) {
  return useQuery({
    queryKey: ["kubernetes", "events", namespace],
    queryFn: () => listKubeEvents(namespace),
    enabled,
    // Server caches cluster reads for 30s; polling faster buys nothing.
    refetchInterval: 30_000,
  });
}
