"use client";

import { useState } from "react";
import { Activity, AlertCircle, ScrollText } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useProjectMetrics } from "@/features/project-overview";
import { projectHealthLine, projectStatsLabel } from "@/features/projects/facts";
import { deriveServiceStatus, useLatestBuilds } from "@/features/services";
import { isProductionGroup } from "@/lib/env-group";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { useProjectUptime } from "@/features/uptime";
import { uptimeTitle } from "@/components/uptime/UptimeBadge";
import { cn, serviceShortName } from "@/lib/utils";
import { ProjectLogsSheet } from "./ProjectLogsSheet";
import { ViewToggle } from "./ViewToggle";
import type { ProjectViewMode } from "./projectViewPref";

// ProjectStatusStrip is the project page's toolbar in the top bar: the
// view toggle, the selected env-group's resource rollup (`kuso project
// metrics`), the health/uptime line (`kuso uptime status`, production-only
// — checks don't run against other groups) and project-wide log search.
// The numbers are short; the full wording is in each title tooltip.
export function ProjectStatusStrip({
  project,
  selectedEnv,
  services,
  envs,
  view,
  onViewChange,
  onOpenService,
}: {
  project: string;
  selectedEnv: string;
  // The selected group's services and envs, as the canvas gets them.
  services: KusoService[];
  envs: KusoEnvironment[];
  view?: ProjectViewMode;
  onViewChange?: (v: ProjectViewMode) => void;
  // Opens a service's overlay (its health strip leads the default tab).
  onOpenService?: (shortName: string) => void;
}) {
  const metrics = useProjectMetrics(project, selectedEnv);
  const uptime = useProjectUptime(project);
  const [logsOpen, setLogsOpen] = useState(false);

  return (
    <div className="flex min-w-0 items-center gap-2 text-xs text-[var(--text-secondary)]">
      {view && onViewChange && <ViewToggle value={view} onChange={onViewChange} />}
      <span className="hidden min-w-0 items-center gap-1.5 lg:inline-flex">
        <MetricsSummary metrics={metrics} env={selectedEnv} />
      </span>
      <HealthSummary
        project={project}
        selectedEnv={selectedEnv}
        services={services}
        envs={envs}
        uptime={uptime}
        onOpenService={onOpenService}
      />
      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-label="Search logs"
        title="Search logs across this project"
        className="h-7 shrink-0 gap-1.5 px-2 text-xs"
        onClick={() => setLogsOpen(true)}
      >
        <ScrollText className="h-3.5 w-3.5" aria-hidden />
        <span className="hidden xl:inline">Logs</span>
      </Button>
      {/* Keyed so the sheet's env filter (seeded once from defaultEnv) resets on env switch. */}
      <ProjectLogsSheet
        key={selectedEnv}
        project={project}
        open={logsOpen}
        onOpenChange={setLogsOpen}
        defaultEnv={selectedEnv}
      />
    </div>
  );
}

function MetricsSummary({ metrics, env }: { metrics: ReturnType<typeof useProjectMetrics>; env: string }) {
  if (metrics.isPending) return <span className="text-[var(--text-tertiary)]">…</span>;
  if (metrics.isError) {
    return (
      <span className="text-[var(--error)]" title={metrics.error instanceof Error ? metrics.error.message : undefined}>
        metrics unavailable
      </span>
    );
  }
  const m = metrics.data;
  if (m.envs === 0) return <span className="text-[var(--text-tertiary)]">nothing deployed</span>;
  if (m.pods === 0) {
    return (
      <span
        className="text-[var(--text-tertiary)]"
        title={`No pod metrics: ${env} is scaled to zero/asleep, or metrics-server isn't reporting.`}
      >
        no pod metrics
      </span>
    );
  }
  const stats = projectStatsLabel({ env, cpuMillicores: m.cpuMillicores, memBytes: m.memBytes, pods: m.pods });
  const short = (p: (typeof stats.parts)[number]) => (p.key === "pods" ? `${m.pods} ${m.pods === 1 ? "pod" : "pods"}` : p.value);
  return (
    <span
      className="truncate whitespace-nowrap tabular-nums"
      title={[`Live usage of ${stats.scope}`, ...stats.parts.map((p) => `${short(p)}: ${p.title}`)].join("\n")}
    >
      {stats.parts.map(short).join(" · ")}
    </span>
  );
}

// One line for coverage and problems: uptime only pings public URLs, so a
// crashing worker or a failed build never shows there; the count of
// failing services comes from the same status the canvas nodes show, with
// a failing ping folded in. Uptime checks only run against production.
function HealthSummary({
  project,
  selectedEnv,
  services,
  envs,
  uptime,
  onOpenService,
}: {
  project: string;
  selectedEnv: string;
  services: KusoService[];
  envs: KusoEnvironment[];
  uptime: ReturnType<typeof useProjectUptime>;
  onOpenService?: (shortName: string) => void;
}) {
  const latestBuilds = useLatestBuilds(project, selectedEnv);
  const rows = services.map((svc) => {
    const mine = envs.filter((e) => e.spec.service === svc.metadata.name);
    const env = mine.find((e) => isProductionGroup(e)) ?? mine[0];
    const short = serviceShortName(project, svc.metadata.name);
    const status = deriveServiceStatus({ stopped: !!svc.spec.stopped, env, latestBuild: latestBuilds.data?.[short] });
    return { short, name: svc.spec.displayName?.trim() || short, status };
  });
  const production = selectedEnv === "production";
  const checks = production && uptime.data?.enabled ? (uptime.data.services ?? []) : null;
  const line = projectHealthLine(rows, checks);
  const uptimeOff = production && uptime.data && !uptime.data.enabled;
  const uptimeError = production && uptime.isError;
  if (!line.text) {
    if (uptimeError) {
      return (
        <span className="text-[var(--error)]" title={uptime.error instanceof Error ? uptime.error.message : undefined}>
          uptime unavailable
        </span>
      );
    }
    return uptimeOff ? <span className="text-[var(--text-tertiary)]">uptime checks off</span> : null;
  }
  const checkOf = (short: string) => checks?.find((c) => c.service === short);
  const title = rows
    .map((r) => {
      const c = checkOf(r.short);
      const ping = c ? uptimeTitle(c) : "not checked";
      return `${r.name}: ${r.status.label}${r.status.detail ? ` (${r.status.detail})` : ""} · ${ping}`;
    })
    .join("\n");
  const tone =
    line.tone === "error" ? "text-[var(--error)]" : line.tone === "warning" ? "text-[var(--warning)]" : "text-[var(--success)]";
  const first = line.problems[0];
  const fullTitle = [line.text, uptimeOff ? "uptime checks off" : "", title].filter(Boolean).join("\n");
  return (
    <span className="inline-flex min-w-0 shrink-0 items-center gap-1.5 whitespace-nowrap" title={fullTitle}>
      <span aria-hidden className="hidden text-[var(--text-tertiary)] lg:inline">·</span>
      {line.tone === "ok" ? (
        <Activity aria-hidden className={cn("h-3 w-3", tone)} />
      ) : (
        <AlertCircle aria-hidden className={cn("h-3 w-3", tone)} />
      )}
      {!line.issues ? (
        <span>uptime ok</span>
      ) : first && onOpenService ? (
        <button
          type="button"
          onClick={() => onOpenService(first)}
          title={`Open ${first}`}
          className={cn(
            "rounded-sm font-medium underline decoration-dotted underline-offset-2 outline-none hover:decoration-solid focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
            tone,
          )}
        >
          {line.issues}
        </button>
      ) : (
        <span className={cn("font-medium", tone)}>{line.issues}</span>
      )}
    </span>
  );
}
