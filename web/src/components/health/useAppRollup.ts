"use client";

import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@/lib/api-client";
import { listAddons, useProjectsSummary } from "@/features/projects";
import { getProjectUptime } from "@/features/uptime";
import { getProjectErrorSummary } from "@/features/services";
import { buildAppRollup, type AppRollup, type ProjectProbe } from "./appRollup";

// Projects probed at once. Each probe is three light requests, so this
// caps the burst at 12 in flight however many projects there are.
const PROBE_CONCURRENCY = 4;

async function probeProject(project: string): Promise<ProjectProbe> {
  const [addons, uptime, errors] = await Promise.all([
    listAddons(project).catch(() => null),
    getProjectUptime(project).catch(() => null),
    getProjectErrorSummary(project, "24h").catch((e: unknown) =>
      e instanceof ApiError && e.status === 404 ? ("unsupported" as const) : null,
    ),
  ]);
  return { project, addons, uptime, errors };
}

export async function probeAll(projects: string[], concurrency = PROBE_CONCURRENCY): Promise<ProjectProbe[]> {
  const out: ProjectProbe[] = new Array(projects.length);
  let next = 0;
  const worker = async () => {
    while (next < projects.length) {
      const i = next++;
      out[i] = await probeProject(projects[i]);
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, projects.length) }, worker));
  return out;
}

export interface AppRollupState {
  rollup?: AppRollup;
  // Service states come from the summary alone; probes fill in the rest.
  probesPending: boolean;
  isPending: boolean;
  error: unknown;
  refetch: () => void;
}

// useAppRollup composes the app-level rollup client-side: the batched
// /api/projects/summary (shared with the projects dashboard cache) plus
// one bounded, slow-polling sweep of per-project probes. A server-side
// rollup endpoint would replace the sweep.
export function useAppRollup(): AppRollupState {
  const summary = useProjectsSummary();
  const projects = (summary.data ?? []).map((i) => i.project.metadata.name).sort();
  const probes = useQuery({
    queryKey: ["health", "app-rollup", "probes", projects.join(",")],
    queryFn: () => probeAll(projects),
    enabled: summary.isSuccess,
    staleTime: 30_000,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
  });

  const rollup = summary.data ? buildAppRollup(summary.data, probes.data ?? []) : undefined;
  return {
    rollup,
    probesPending: probes.isPending,
    isPending: summary.isPending,
    error: summary.error,
    refetch: () => void summary.refetch(),
  };
}
