"use client";

import { useQuery } from "@tanstack/react-query";
import { useCan, Perms } from "@/features/auth";
import { listIngressDomains } from "./api";

// Admin-only on the server; non-admins get no data and no hint.
export function useIngressDomains() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  return useQuery({
    queryKey: ["kubernetes", "domains"],
    queryFn: listIngressDomains,
    enabled: isAdmin,
    staleTime: 30_000,
  });
}
