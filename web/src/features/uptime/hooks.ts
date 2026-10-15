"use client";

import { useQuery } from "@tanstack/react-query";
import { getProjectUptime } from "./api";

export function useProjectUptime(project: string) {
  return useQuery({
    queryKey: ["uptime", project],
    queryFn: () => getProjectUptime(project),
    enabled: !!project,
    refetchInterval: 30_000,
  });
}
