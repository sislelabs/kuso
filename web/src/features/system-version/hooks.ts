"use client";

import { useQuery } from "@tanstack/react-query";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";

export interface SystemVersionSummary {
  current: string;
  latest: string;
  needsUpdate: boolean;
}

// Same key as the updates page so every surface reads one cache entry.
export const systemVersionQueryKey = ["system", "version"] as const;

// useSystemVersion is the admin-only "is there a newer kuso?" probe.
// Non-admins can't call the endpoint, so it stays disabled for them.
export function useSystemVersion() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  return useQuery<SystemVersionSummary>({
    queryKey: systemVersionQueryKey,
    queryFn: () => api("/api/system/version"),
    enabled: isAdmin,
    staleTime: 5 * 60_000,
    retry: false,
    throwOnError: false,
  });
}
