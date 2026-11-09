"use client";

import { useState } from "react";
import { Activity, AlertCircle, Boxes, Cpu, MemoryStick, ScrollText } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useProjectMetrics } from "@/features/project-overview";
import { envScopeLabel, projectStatsLabel, serviceIssues } from "@/features/projects/facts";
import { deriveServiceStatus, useLatestBuilds } from "@/features/services";
import { isProductionGroup } from "@/lib/env-group";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { summarizeUptime, useProjectUptime } from "@/features/uptime";
import { uptimeTitle } from "@/components/uptime/UptimeBadge";
import { cn, serviceShortName } from "@/lib/utils";
import { ProjectLogsSheet } from "./ProjectLogsSheet";
import { ViewToggle } from "./ViewToggle";
import type { ProjectViewMode } from "./projectViewPref";

// ProjectStatusStrip sits above the canvas: the selected env-group's
// resource rollup (`kuso project metrics`), uptime summary (`kuso uptime
// status`, production-only — checks don't run against other groups) and
// the entry point to project-wide log search.
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
    <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b border-[var(--border-subtle)] px-6 py-1.5 text-xs text-[var(--text-secondary)]">
      {view && onViewChange && <ViewToggle value={view} onChange={onViewChange} />}
      <MetricsSummary metrics={metrics} env={selectedEnv} />
      {selectedEnv === "production" && <UptimeSummary uptime={uptime} onOpenService={onOpenService} />}
      <ServiceIssuesSummary
        project={project}
        selectedEnv={selectedEnv}
        services={services}
        envs={envs}
        onOpenService={onOpenService}
      />
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="ml-auto h-7 gap-1.5 px-2 text-xs"
        onClick={() => setLogsOpen(true)}
      >
        <ScrollText className="h-3.5 w-3.5" />
        Search logs
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
  if (metrics.isPending) return <span className="text-[var(--text-tertiary)]">{envScopeLabel(env)}: …</span>;
  if (metrics.isError) {
    return (
      <span className="text-[var(--error)]" title={metrics.error instanceof Error ? metrics.error.message : undefined}>
        metrics unavailable
      </span>
    );
  }
  const m = metrics.data;
  if (m.envs === 0) return <span className="text-[var(--text-tertiary)]">{envScopeLabel(env)}: nothing deployed</span>;
  if (m.pods === 0) {
    return (
      <span
        className="text-[var(--text-tertiary)]"
        title={`No pod metrics: ${env} is scaled to zero/asleep, or metrics-server isn't reporting.`}
      >
        {envScopeLabel(env)}: no pod metrics
      </span>
    );
  }
  const stats = projectStatsLabel({ env, cpuMillicores: m.cpuMillicores, memBytes: m.memBytes, pods: m.pods });
  const ICON = { cpu: Cpu, mem: MemoryStick, pods: Boxes, uptime: Activity } as const;
  return (
    <span className="inline-flex items-center gap-3" aria-label={stats.text}>
      <span className="font-medium text-[var(--text-secondary)]" title={`Live usage of the ${stats.scope} environment`}>
        {stats.scope}
      </span>
      {stats.parts.map((p) => {
        const Icon = ICON[p.key];
        return (
          <span key={p.key} className="inline-flex items-center gap-1" title={p.title}>
            <Icon aria-hidden className="h-3 w-3 text-[var(--text-tertiary)]" />
            {p.label && <span className="text-[var(--text-tertiary)]">{p.label}</span>}
            <span className="tabular-nums">{p.value}</span>
          </span>
        );
      })}
    </span>
  );
}

function UptimeSummary({
  uptime,
  onOpenService,
}: {
  uptime: ReturnType<typeof useProjectUptime>;
  onOpenService?: (shortName: string) => void;
}) {
  if (uptime.isPending) return null;
  if (uptime.isError) {
    return (
      <span className="text-[var(--error)]" title={uptime.error instanceof Error ? uptime.error.message : undefined}>
        uptime unavailable
      </span>
    );
  }
  const services = uptime.data.services ?? [];
  if (!uptime.data.enabled) return <span className="text-[var(--text-tertiary)]">uptime checks off</span>;
  const s = summarizeUptime(services);
  // Only checks that are on and have a result count toward "N/M up".
  if (s.checked === 0) return null;
  const bad = services.filter((x) => x.state === "down" || x.state === "failing");
  return (
    <span
      className="inline-flex items-center gap-1.5"
      title={services.map((x) => `${x.service}: ${uptimeTitle(x)}`).join("\n")}
    >
      <Activity
        className={cn(
          "h-3 w-3",
          s.down > 0 ? "text-[var(--error)]" : s.failing > 0 ? "text-[var(--warning)]" : "text-[var(--success)]",
        )}
      />
      <span className="tabular-nums">
        {s.up}/{s.checked} uptime {s.checked === 1 ? "check" : "checks"} up
      </span>
      {bad.length > 0 && (
        <span className="inline-flex flex-wrap items-center gap-x-1">
          <span aria-hidden>·</span>
          {bad.map((x, i) => {
            const text = `${x.service} ${x.state}`;
            const tone = x.state === "down" ? "text-[var(--error)]" : "text-[var(--warning)]";
            return (
              <span key={`${x.service}-${x.env}`} className="inline-flex items-center">
                {onOpenService ? (
                  <button
                    type="button"
                    onClick={() => onOpenService(x.service)}
                    title={`Open ${x.service}: ${uptimeTitle(x)}`}
                    className={cn(
                      "rounded-sm underline decoration-dotted underline-offset-2 outline-none hover:decoration-solid focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
                      tone,
                    )}
                  >
                    {text}
                  </button>
                ) : (
                  <span className={tone}>{text}</span>
                )}
                {i < bad.length - 1 && <span aria-hidden>,</span>}
              </span>
            );
          })}
        </span>
      )}
    </span>
  );
}

// Uptime only pings public URLs; a crashing worker or a failed build
// never shows there. This counts services the way the canvas nodes do.
function ServiceIssuesSummary({
  project,
  selectedEnv,
  services,
  envs,
  onOpenService,
}: {
  project: string;
  selectedEnv: string;
  services: KusoService[];
  envs: KusoEnvironment[];
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
  const issues = serviceIssues(rows.map((r) => r.status));
  if (!issues.text) return null;
  const bad = rows.filter((r) => r.status.tone === "error" || r.status.kind === "degraded");
  const first = bad[0];
  const tone = issues.tone === "error" ? "text-[var(--error)]" : "text-[var(--warning)]";
  const title = bad.map((r) => `${r.name}: ${r.status.label}${r.status.detail ? ` (${r.status.detail})` : ""}`).join("\n");
  return (
    <span className="inline-flex items-center gap-1.5" title={title}>
      <AlertCircle aria-hidden className={cn("h-3 w-3", tone)} />
      {first && onOpenService ? (
        <button
          type="button"
          onClick={() => onOpenService(first.short)}
          className={cn(
            "rounded-sm underline decoration-dotted underline-offset-2 outline-none hover:decoration-solid focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
            tone,
          )}
        >
          {issues.text}
        </button>
      ) : (
        <span className={tone}>{issues.text}</span>
      )}
    </span>
  );
}
