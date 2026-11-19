"use client";

import { useQuery } from "@tanstack/react-query";
import { getProjectMetrics } from "./api";

export function useProjectMetrics(project: string, env = "production") {
  return useQuery({
    queryKey: ["project-metrics", project, env],
    queryFn: () => getProjectMetrics(project, env),
    enabled: !!project,
    refetchInterval: 30_000,
  });
}
