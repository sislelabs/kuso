"use client";

import { useState } from "react";
import { Activity, Cpu, MemoryStick, ScrollText } from "lucide-react";
import { Button } from "@/components/ui/button";
import { formatBytes, formatMillicores, useProjectMetrics } from "@/features/project-overview";
import { summarizeUptime, useProjectUptime } from "@/features/uptime";
import { uptimeTitle } from "@/components/uptime/UptimeBadge";
import { cn } from "@/lib/utils";
import { ProjectLogsSheet } from "./ProjectLogsSheet";

// ProjectStatusStrip sits above the canvas: production resource rollup
// (`kuso project metrics`), uptime summary (`kuso uptime status`) and
// the entry point to project-wide log search.
export function ProjectStatusStrip({ project, selectedEnv }: { project: string; selectedEnv: string }) {
  const metrics = useProjectMetrics(project);
  const uptime = useProjectUptime(project);
  const [logsOpen, setLogsOpen] = useState(false);

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b border-[var(--border-subtle)] px-6 py-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
      <MetricsSummary metrics={metrics} />
      <UptimeSummary uptime={uptime} />
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="ml-auto h-6 gap-1 px-2 font-mono text-[11px]"
        onClick={() => setLogsOpen(true)}
      >
        <ScrollText className="h-3.5 w-3.5" />
        Search logs
      </Button>
      <ProjectLogsSheet
        project={project}
        open={logsOpen}
        onOpenChange={setLogsOpen}
        defaultEnv={selectedEnv}
      />
    </div>
  );
}

function MetricsSummary({ metrics }: { metrics: ReturnType<typeof useProjectMetrics> }) {
  if (metrics.isPending) return <span className="text-[var(--text-tertiary)]">production: …</span>;
  if (metrics.isError) {
    return (
      <span className="text-[var(--error)]" title={metrics.error instanceof Error ? metrics.error.message : undefined}>
        metrics unavailable
      </span>
    );
  }
  const m = metrics.data;
  if (m.envs === 0) return <span className="text-[var(--text-tertiary)]">production: nothing deployed</span>;
  if (m.pods === 0) {
    return (
      <span
        className="text-[var(--text-tertiary)]"
        title="No pod metrics: production is scaled to zero/asleep, or metrics-server isn't reporting."
      >
        production: no pod metrics
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-3" title={`Summed across ${m.envs} production env${m.envs === 1 ? "" : "s"}`}>
      <span className="text-[var(--text-tertiary)]">production</span>
      <span className="inline-flex items-center gap-1">
        <Cpu className="h-3 w-3 text-[var(--text-tertiary)]" />
        {formatMillicores(m.cpuMillicores)}
      </span>
      <span className="inline-flex items-center gap-1">
        <MemoryStick className="h-3 w-3 text-[var(--text-tertiary)]" />
        {formatBytes(m.memBytes)}
      </span>
      <span>
        {m.pods} pod{m.pods === 1 ? "" : "s"}
      </span>
    </span>
  );
}

function UptimeSummary({ uptime }: { uptime: ReturnType<typeof useProjectUptime> }) {
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
  if (services.length === 0) return null;
  const s = summarizeUptime(services);
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
      <span>
        {s.up}/{services.length} up
      </span>
      {bad.length > 0 && (
        <span className={s.down > 0 ? "text-[var(--error)]" : "text-[var(--warning)]"}>
          · {bad.map((x) => `${x.service} ${x.state}`).join(", ")}
        </span>
      )}
    </span>
  );
}
