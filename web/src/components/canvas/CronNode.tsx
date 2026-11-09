"use client";

import { Clock, Globe, Terminal, Box, MoreHorizontal } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { describeCron, formatAbsoluteTime } from "@/lib/format-time";
import { nextRuns } from "@/lib/cron-next";
import { cn } from "@/lib/utils";
import { deriveCronStatus, STATUS_TONE } from "@/features/services";
import { NodeHandles } from "./NodeHandles";
import { nodeFocusClass, type CanvasNodeA11y } from "./nodeA11y";
import { CRON_NODE_CHROME, smallNodeWidth } from "./geometry";
import { nodeActionButtonClass, nodeCardClass, nodeNameClass } from "./nodeStyle";

// Canvas node for KusoCron. Smaller than the service/addon nodes so
// crons read as a different class of object at a glance — they're
// supporting infrastructure, not the main pipeline. Same side handles
// so drag-to-connect can wire a cron's HTTP probe target to a sibling
// service node visually.

export interface CronNodeData extends CanvasNodeA11y {
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
      // Resolved runtime detail — what the CronJob's pod actually runs
      // with. For kind=service these are baked in from the parent
      // service's production env at create/sync time (so they can drift
      // from the service until re-synced); the edit dialog surfaces them.
      image?: { repository?: string; tag?: string; pullPolicy?: string };
      envFromSecrets?: string[];
      concurrencyPolicy?: string;
      activeDeadlineSeconds?: number;
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

// The cron's schedule in words with its zone ("Daily at 23:00 UTC"), and
// a tooltip with the raw expression and the next run in both zones.
export function describeSchedule(cron: string): string {
  return describeCron(cron);
}

export function scheduleTitle(cron: string): string {
  const next = nextRuns(cron, 1)[0];
  if (!next) return cron;
  return `${cron} · next run ${formatAbsoluteTime(next, { timeZone: "UTC" })} (${formatAbsoluteTime(next)} your time)`;
}

// summary returns a one-line "what does this cron actually do" hint
// to render below the schedule row. Truncates aggressively so the
// 200px-wide tile doesn't grow.
function summary(project: string, spec: CronNodeData["cron"]["spec"]): string {
  switch ((spec.kind || "service").toLowerCase()) {
    case "http":
      return spec.url ? spec.url.replace(/^https?:\/\//, "") : "(no url)";
    case "command":
      return (spec.command ?? []).join(" ") || "(no command)";
    case "service":
      return spec.service ? `→ ${shortName(project, spec.service)}` : "(no service)";
    default:
      return "";
  }
}

export function CronNode({ id, data }: { id: string; data: CronNodeData }) {
  const kind = (data.cron.spec.kind || "service").toLowerCase();
  const Icon = KIND_ICONS[kind] ?? Clock;
  const displayName =
    data.cron.spec.displayName?.trim() || shortName(data.project, data.cron.metadata.name);
  const schedule = data.cron.spec.schedule ?? "";
  const suspended = !!data.cron.spec.suspend;
  const detail = summary(data.project, data.cron.spec);

  const status = deriveCronStatus(data.cron.spec);
  const tone = STATUS_TONE[status.tone];
  const kindLabel = kind === "http" ? "HTTP" : kind === "command" ? "Command" : "Service";

  return (
    <div
      data-node-context
      data-canvas-node={id}
      tabIndex={data.__tabIndex ?? -1}
      role="group"
      aria-roledescription="cron job"
      aria-label={`${displayName}, ${suspended ? "paused" : describeSchedule(schedule)}`}
      onContextMenu={data.__onContext}
      style={{ width: smallNodeWidth(displayName, CRON_NODE_CHROME) }}
      className={cn(
        // 220×96: smaller than the 280×120 service tiles so the visual
        // hierarchy stays. Crons have no live health, so the border stays
        // neutral; paused crons dim.
        nodeCardClass,
        // Dashed: a cron is a schedule, not a running thing.
        "flex h-[96px] flex-col border-dashed p-2.5",
        suspended
          ? "opacity-70 border-[var(--border-default)]"
          : "border-[var(--border-strong)]/70 hover:border-[var(--border-strong)]",
        nodeFocusClass,
      )}
    >
      <NodeHandles />
      <button
        tabIndex={-1}
        type="button"
        aria-label={`Actions for ${displayName}`}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          data.__onContext?.(e);
        }}
        className={nodeActionButtonClass}
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>

      <div className="flex items-center gap-1.5 pr-7">
        <Icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
        <span className={nodeNameClass} title={displayName}>{displayName}</span>
      </div>

      <div className="mt-1 truncate font-mono text-xs text-[var(--text-secondary)]" title={detail}>
        {detail}
      </div>

      <div className="mt-auto flex items-center justify-between gap-2 text-xs text-[var(--text-tertiary)]">
        <span className="flex min-w-0 items-center gap-1.5" title={scheduleTitle(schedule)}>
          <span aria-hidden className={cn("h-1.5 w-1.5 shrink-0 rounded-full", tone.dot)} />
          <span className="truncate">
            {suspended ? "Paused · " : ""}
            {describeSchedule(schedule)}
          </span>
        </span>
        <span className="shrink-0">{kindLabel}</span>
      </div>
    </div>
  );
}
