"use client";

import { useMemo } from "react";
import { useQueries } from "@tanstack/react-query";
import type { KusoService } from "@/types/projects";
import { useProjectVulns } from "@/features/vulns";
import { errorsQueryKey, listErrors, useProjectErrorSummary } from "@/features/services";
import { ApiError } from "@/lib/api-client";
import { serviceShortName } from "@/lib/utils";
import { canvasVulnSummaries, summarizeErrors, type ErrorSummary } from "./badgeSummary";

// useServiceBadges feeds the "N errors · 24h" and vulnerability badges
// on the canvas nodes and the mobile service cards from the same
// queries, so the two surfaces can't disagree.
export function useServiceBadges(project: string, selectedEnv: string, services: readonly KusoService[]) {
  // One rollup request per project; every node reads its badge from it.
  const projectVulns = useProjectVulns(project);
  const vulnSummaries = useMemo(
    () => canvasVulnSummaries(projectVulns.data?.images ?? [], project, selectedEnv),
    [projectVulns.data, project, selectedEnv],
  );

  // 24h error counts per service: one project-wide summary request.
  // Servers without the summary endpoint 404 it; only then fall back to
  // one small request per service (sharing the Errors tab's cache key).
  const serviceShorts = useMemo(
    () => services.map((s) => serviceShortName(project, s.metadata.name)),
    [services, project],
  );
  const errorSummary = useProjectErrorSummary(project);
  const perServiceErrors =
    errorSummary.error instanceof ApiError && (errorSummary.error.status === 404 || errorSummary.error.status === 405);
  const errorQueries = useQueries({
    queries: (perServiceErrors ? serviceShorts : []).map((short) => ({
      queryKey: errorsQueryKey(project, short, "24h", ""),
      queryFn: () => listErrors(project, short, "24h"),
      staleTime: 30_000,
      refetchInterval: 60_000,
      refetchIntervalInBackground: false,
      retry: false,
    })),
  });
  const errorDataKey = errorQueries.map((q) => q.dataUpdatedAt).join(",");
  const errorSummaries = useMemo(() => {
    const out: Record<string, ErrorSummary | undefined> = {};
    if (perServiceErrors) {
      serviceShorts.forEach((short, i) => {
        out[short] = summarizeErrors(errorQueries[i]?.data);
      });
      return out;
    }
    const data = errorSummary.data;
    if (!data) return out;
    for (const short of serviceShorts) {
      const s = data.services[short];
      out[short] = s ? { occurrences: s.occurrences, groups: s.groups } : { occurrences: 0, groups: 0 };
    }
    return out;
    // errorQueries is a fresh array each render; key off the data stamps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serviceShorts, errorDataKey, perServiceErrors, errorSummary.data]);  return { vulnSummaries, errorSummaries };
}
