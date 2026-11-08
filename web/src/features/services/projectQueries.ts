"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import type { BuildSummary } from "./api";
import type { CronNodeSpec } from "./cronSpec";

const IN_FLIGHT = ["queued", "pending", "running", "deploying"];

// useLatestBuilds is the per-project "newest build per service" map for
// one env group, keyed by short service name. Shared by the canvas, the
// list view and the mobile view (same query key → one poller). Polls at
// 5s only while a build is in flight, 15s otherwise, paused when hidden.
export function useLatestBuilds(project: string, envGroup: string) {
  return useQuery<Record<string, BuildSummary>>({
    queryKey: ["projects", project, "builds", "latest", envGroup],
    queryFn: () =>
      api(
        `/api/projects/${encodeURIComponent(project)}/builds/latest?env=${encodeURIComponent(envGroup)}`,
      ),
    enabled: !!project,
    refetchInterval: (q) =>
      Object.values(q.state.data ?? {}).some((b) => IN_FLIGHT.includes((b.status ?? "").toLowerCase()))
        ? 5_000
        : 15_000,
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });
}

export interface ProjectCron {
  metadata: { name: string };
  spec: CronNodeSpec;
}

// useProjectCrons lists every KusoCron in the project (service-attached
// and project-scoped).
export function useProjectCrons(project: string) {
  return useQuery<ProjectCron[]>({
    queryKey: ["projects", project, "crons"],
    queryFn: () => api(`/api/projects/${encodeURIComponent(project)}/crons`),
    enabled: !!project,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    staleTime: 10_000,
  });
}
