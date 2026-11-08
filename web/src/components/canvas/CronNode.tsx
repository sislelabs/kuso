"use client";

import { Handle, Position } from "@xyflow/react";
import { Clock, Globe, Terminal, Box, MoreHorizontal } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { deriveCronStatus, STATUS_TONE } from "@/features/services";

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

const DOW_LABEL = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// describeSchedule recognises the four preset shapes that CronPicker
// emits and turns them back into a human-readable string. Anything
// else falls back to the raw cron expression. Mirror of the picker's
// parseSchedule — kept duplicated rather than shared because the
// picker doesn't export its parser shape today.
export function describeSchedule(cron: string): string {
  const s = cron.trim();
  if (!s) return "—";
  const fields = s.split(/\s+/);
  if (fields.length !== 5) return s;
  const [mn, hr, dom, mo, dow] = fields;
  const isInt = (v: string, lo: number, hi: number) => {
    if (!/^\d+$/.test(v)) return false;
    const n = parseInt(v, 10);
    return n >= lo && n <= hi;
  };
  const pad = (v: string) => (v.length === 1 ? "0" + v : v);
  if (isInt(mn, 0, 59) && hr === "*" && dom === "*" && mo === "*" && dow === "*") {
    return `hourly :${pad(mn)}`;
  }
  if (
    isInt(mn, 0, 59) &&
    isInt(hr, 0, 23) &&
    dom === "*" &&
    mo === "*" &&
    dow === "*"
  ) {
    return `daily ${pad(hr)}:${pad(mn)}`;
  }
  if (
    isInt(mn, 0, 59) &&
    isInt(hr, 0, 23) &&
    dom === "*" &&
    mo === "*" &&
    isInt(dow, 0, 6)
  ) {
    return `${DOW_LABEL[parseInt(dow, 10)]} ${pad(hr)}:${pad(mn)}`;
  }
  if (
    isInt(mn, 0, 59) &&
    isInt(hr, 0, 23) &&
    isInt(dom, 1, 31) &&
    mo === "*" &&
    dow === "*"
  ) {
    return `${dom}th ${pad(hr)}:${pad(mn)}`;
  }
  return s;
}

// summary returns a one-line "what does this cron actually do" hint
// to render below the schedule row. Truncates aggressively so the
// 200px-wide tile doesn't grow.
function summary(spec: CronNodeData["cron"]["spec"]): string {
  switch ((spec.kind || "service").toLowerCase()) {
    case "http":
      return spec.url ? spec.url.replace(/^https?:\/\//, "") : "(no url)";
    case "command":
      return (spec.command ?? []).join(" ") || "(no command)";
    case "service":
      return spec.service ? `→ ${spec.service}` : "(no service)";
    default:
      return "";
  }
}

export function CronNode({ data }: { data: CronNodeData }) {
  const kind = (data.cron.spec.kind || "service").toLowerCase();
  const Icon = KIND_ICONS[kind] ?? Clock;
  const displayName =
    data.cron.spec.displayName?.trim() || shortName(data.project, data.cron.metadata.name);
  const schedule = data.cron.spec.schedule ?? "";
  const suspended = !!data.cron.spec.suspend;
  const detail = summary(data.cron.spec);

  const status = deriveCronStatus(data.cron.spec);
  const tone = STATUS_TONE[status.tone];
  const kindLabel = kind === "http" ? "HTTP" : kind === "command" ? "Command" : "Service";

  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        // 220×96: smaller than the 280×120 service tiles so the visual
        // hierarchy stays. Crons have no live health, so the border stays
        // neutral; paused crons dim.
        "group relative flex h-[96px] w-[220px] flex-col rounded-2xl border-2 bg-[var(--bg-elevated)] p-2.5 transition-colors cursor-pointer",
        suspended
          ? "opacity-70 border-[var(--border-default)]"
          : "border-[var(--border-subtle)] hover:border-[var(--border-strong)]",
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />
      <button
        type="button"
        aria-label={`Actions for ${displayName}`}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          data.__onContext?.(e);
        }}
        className="nodrag absolute right-1.5 top-1.5 z-10 inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] opacity-0 transition-opacity hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100"
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>

      <div className="flex items-center gap-1.5 pr-7">
        <Icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" aria-hidden />
        <span className="truncate text-sm font-medium text-[var(--text-primary)]">{displayName}</span>
      </div>

      <div className="mt-1 truncate font-mono text-xs text-[var(--text-secondary)]" title={detail}>
        {detail}
      </div>

      <div className="mt-auto flex items-center justify-between gap-2 text-xs text-[var(--text-tertiary)]">
        <span className="flex min-w-0 items-center gap-1.5" title={schedule}>
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
