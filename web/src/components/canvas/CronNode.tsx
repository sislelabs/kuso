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

  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        // 220×96 — bumped from 200×72 to fit a target/command preview
        // line under the schedule. Still visibly smaller than the
        // 280×120 service tiles so the visual hierarchy stays.
        "group flex h-[96px] w-[220px] flex-col rounded-2xl border-2 bg-[var(--bg-elevated)] p-2.5 transition-colors cursor-pointer",
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

      {/* Target/command preview — gives users a hint of "what is this
          cron actually pointing at?" without opening the overlay.
          Truncated; the overlay shows the full thing. */}
      <div
        className="mt-1 truncate font-mono text-[10px] text-[var(--text-secondary)]"
        title={detail}
      >
        {detail}
      </div>

      <div className="mt-auto flex items-center justify-between gap-2 text-[10px] text-[var(--text-tertiary)]">
        <span className="truncate" title={schedule}>
          <Clock className="mr-1 inline h-2.5 w-2.5" />
          {describeSchedule(schedule)}
        </span>
        <span className="font-mono text-[9px] uppercase tracking-wider">{kind}</span>
      </div>
    </div>
  );
}
