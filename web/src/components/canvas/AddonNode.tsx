"use client";

import { Handle, Position } from "@xyflow/react";
import { MoreHorizontal } from "lucide-react";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import type { KusoAddon } from "@/types/projects";
import { cn } from "@/lib/utils";
import { deriveAddonStatus, STATUS_TONE } from "@/features/services";
import { VulnBadge } from "./NodeBadges";
import type { VulnSummary } from "./badgeSummary";

export interface AddonNodeData extends Record<string, unknown> {
  project: string;
  addon: KusoAddon;
  vulns?: VulnSummary;
  __onContext?: (e: React.MouseEvent) => void;
  __onOpenTab?: (tab: string) => void;
}

export function AddonNode({ data }: { data: AddonNodeData }) {
  const status = deriveAddonStatus(data.addon);
  const tone = STATUS_TONE[status.tone];
  const shortName = data.addon.metadata.name.startsWith(data.project + "-")
    ? data.addon.metadata.name.slice(data.project.length + 1)
    : data.addon.metadata.name;
  const meta = [
    addonLabel(data.addon.spec.kind) + (data.addon.spec.version ? ` ${data.addon.spec.version}` : ""),
    data.addon.spec.storageSize,
    data.addon.spec.ha ? "HA" : undefined,
  ].filter(Boolean);
  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      title={status.detail ? `${status.label}: ${status.detail}` : undefined}
      className={cn(
        // 3 grid units tall (services are 5) so addons read as supporting
        // pieces; both snap to the same 24px grid.
        "group relative flex h-[72px] w-[220px] flex-col justify-center rounded-2xl border-2 bg-[var(--bg-elevated)] px-3 py-2 transition-colors cursor-pointer hover:border-[var(--border-strong)]",
        tone.border,
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />
      <button
        type="button"
        aria-label={`Actions for ${shortName}`}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          data.__onContext?.(e);
        }}
        className="nodrag absolute right-1.5 top-1.5 z-10 inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] opacity-0 transition-opacity hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100"
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>
      <div className="flex items-center gap-2.5 pr-6">
        <AddonIcon kind={data.addon.spec.kind} className="h-5 w-5 shrink-0" />
        <div className="min-w-0 flex-1">
          <p className="flex min-w-0 items-center gap-1.5 text-sm font-medium text-[var(--text-primary)]">
            <span className="truncate" title={data.addon.metadata.name}>{shortName}</span>
            <VulnBadge summary={data.vulns} onOpen={() => data.__onOpenTab?.("vulnerabilities")} />
          </p>
          <p className="mt-0.5 flex min-w-0 items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
            <span aria-hidden className={cn("h-1.5 w-1.5 shrink-0 rounded-full", tone.dot)} />
            <span className={cn("shrink-0", status.tone === "success" ? "text-[var(--text-secondary)]" : tone.text)}>
              {status.label}
            </span>
            <span className="truncate">· {meta.join(" · ")}</span>
          </p>
        </div>
      </div>
    </div>
  );
}
