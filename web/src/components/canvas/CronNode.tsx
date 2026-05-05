"use client";

import { Handle, Position } from "@xyflow/react";
import { Clock, Globe, Terminal, Box } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

// Canvas node for KusoCron. Smaller than the service/addon nodes so
// crons read as a different class of object at a glance — they're
// supporting infrastructure, not the main pipeline. Same handle
// positions (left=target, right=source) so drag-to-connect can wire
// a cron's HTTP probe target to a sibling service node visually.

export interface CronNodeData {
  project: string;
  cron: {
    metadata: { name: string };
    spec: {
      project?: string;
      kind?: string;
      service?: string;
      url?: string;
      schedule?: string;
      command?: string[];
      suspend?: boolean;
      displayName?: string;
    };
  };
  __onContext?: (e: React.MouseEvent) => void;
}

const KIND_ICONS: Record<string, LucideIcon> = {
  http: Globe,
  command: Terminal,
  service: Box,
};

function shortName(project: string, fqn: string): string {
  // Project-scoped cron: "<project>-<short>"
  // Service-attached cron: "<project>-<svc>-<short>"
  // Either way, strip the leading project prefix; the user reads the
  // tail as the cron's identifier.
  const prefix = project + "-";
  return fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
}

export function CronNode({ data }: { data: CronNodeData }) {
  const kind = (data.cron.spec.kind || "service").toLowerCase();
  const Icon = KIND_ICONS[kind] ?? Clock;
  const displayName =
    data.cron.spec.displayName?.trim() || shortName(data.project, data.cron.metadata.name);
  const schedule = data.cron.spec.schedule ?? "";
  const suspended = !!data.cron.spec.suspend;

  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        // 200×72 — visibly smaller than the 280×120 service/addon
        // tiles. Two grid units shorter on each axis so a project
        // with many crons stays scannable.
        "group flex h-[72px] w-[200px] flex-col rounded-2xl border-2 bg-[var(--bg-elevated)] p-2.5 transition-colors cursor-pointer",
        suspended
          ? "opacity-50 border-[var(--border-strong)]"
          : "border-[var(--border-strong)] hover:border-[var(--accent)]/60",
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />

      <div className="flex items-center gap-1.5">
        <Icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
        <span className="truncate text-[12px] font-medium">{displayName}</span>
        {suspended && (
          <span className="ml-auto rounded bg-[var(--bg-tertiary)] px-1 py-0 font-mono text-[9px] uppercase text-[var(--text-tertiary)]">
            paused
          </span>
        )}
      </div>

      <div className="mt-auto flex items-center justify-between gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
        <span className="truncate" title={schedule}>
          {schedule || "—"}
        </span>
        <span className="text-[9px] uppercase tracking-wider">{kind}</span>
      </div>
    </div>
  );
}
