"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { projectErrorSummaryQueryKey } from "@/features/services";
import type { ProjectErrorSummary } from "@/features/services/api";
import type { KusoService } from "@/types/projects";
import { useProjectVulns } from "@/features/vulns";
import { ERRORS_WINDOW, errorsEnvScope, useProjectErrorCounts } from "@/features/errors";
import { serviceShortName } from "@/lib/utils";
import { canvasVulnSummaries, errorChipHot } from "./badgeSummary";

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

  // Error counts come from the shared project summary (features/errors),
  // the same cache the overlay tab badge, Errors tab and project card read.
  const serviceShorts = useMemo(
    () => services.map((s) => serviceShortName(project, s.metadata.name)),
    [services, project],
  );
  const envScope = errorsEnvScope(selectedEnv);
  const { counts: errorSummaries } = useProjectErrorCounts(project, serviceShorts, ERRORS_WINDOW, envScope);
  // The last hour, from the same summary endpoint, grades the chip (new /
  // spiking vs a steady trickle). Older servers without the summary don't
  // get a per-service fallback here: the chip just grades on the day total.
  const lastHour = useQuery({
    queryKey: [...projectErrorSummaryQueryKey(project, "1h"), envScope],
    queryFn: () =>
      api<ProjectErrorSummary>(
        `/api/projects/${encodeURIComponent(project)}/errors/summary?since=1h${envScope ? `&env=${encodeURIComponent(envScope)}` : ""}`,
      ),
    enabled: !!project,
    staleTime: 30_000,
    refetchInterval: (q) => (q.state.status === "error" ? false : 60_000),
    refetchIntervalInBackground: false,
    retry: false,
  });
  const errorHot = useMemo(() => {
    const out: Record<string, boolean> = {};
    for (const short of serviceShorts) {
      const day = errorSummaries[short]?.occurrences ?? 0;
      const hour = lastHour.data ? (lastHour.data.services[short]?.occurrences ?? 0) : undefined;
      out[short] = errorChipHot(day, hour);
    }
    return out;
  }, [serviceShorts, errorSummaries, lastHour.data]);
  return { vulnSummaries, errorSummaries, errorHot };
}
