"use client";

import { LogViewer } from "@/components/logs/LogViewer";

interface Props {
  project: string;
  service: string;
  // Env-group of the env the overlay is showing (production / staging /
  // preview-pr-N). Scopes history queries so staging doesn't mix in
  // production lines.
  defaultEnv?: string;
  // Env CR name for the live tail. Omitted → history only.
  streamEnv?: string;
  // Switches the overlay to Deployments (the empty state's "View build logs").
  onOpenDeployments?: () => void;
}

// ServiceLogsPanel is one logs view: it opens on the live tail and the
// same search box / range picker switch it to the 7-day archive. View
// state (query, range, filters, scroll) survives tab switches.
export function ServiceLogsPanel({ project, service, defaultEnv = "", streamEnv, onOpenDeployments }: Props) {
  return (
    <LogViewer
      project={project}
      service={service}
      streamEnv={streamEnv}
      defaultEnv={defaultEnv}
      onOpenDeployments={onOpenDeployments}
      className="h-[calc(100dvh-13rem)] min-h-[24rem]"
    />
  );
}
