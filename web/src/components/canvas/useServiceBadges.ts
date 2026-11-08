"use client";

import { useMemo } from "react";
import type { KusoService } from "@/types/projects";
import { useProjectVulns } from "@/features/vulns";
import { useProjectErrorCounts } from "@/features/errors";
import { serviceShortName } from "@/lib/utils";
import { canvasVulnSummaries } from "./badgeSummary";

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
  const { counts: errorSummaries } = useProjectErrorCounts(project, serviceShorts);
  return { vulnSummaries, errorSummaries };
}
