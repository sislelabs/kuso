"use client";

import { useQuery } from "@tanstack/react-query";
import { listAddonPods, listServicePods } from "./api";

export function useServicePods(project: string, service: string, env: string | undefined) {
  return useQuery({
    queryKey: ["pods", "service", project, service, env ?? ""],
    queryFn: () => listServicePods(project, service, env ?? ""),
    enabled: !!project && !!service && !!env,
    refetchInterval: 15_000,
  });
}

export function useAddonPods(project: string, addon: string) {
  return useQuery({
    queryKey: ["pods", "addon", project, addon],
    queryFn: () => listAddonPods(project, addon),
    enabled: !!project && !!addon,
    refetchInterval: 15_000,
  });
}
