"use client";

import { useQuery } from "@tanstack/react-query";
import { listServiceAudit } from "./api";

export function useServiceAudit(project: string, phase: string, service: string, enabled = true) {
  return useQuery({
    queryKey: ["audit", "app", project, phase, service],
    queryFn: () => listServiceAudit(project, phase, service),
    enabled: enabled && !!project && !!phase && !!service,
  });
}
