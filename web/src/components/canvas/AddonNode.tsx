"use client";

import { MoreHorizontal } from "lucide-react";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import type { KusoAddon } from "@/types/projects";
import { cn } from "@/lib/utils";
import { deriveAddonStatus, STATUS_TONE } from "@/features/services";
import { VulnBadge } from "./NodeBadges";
import type { VulnSummary } from "./badgeSummary";
import { NodeHandles } from "./NodeHandles";
import { nodeFocusClass, type CanvasNodeA11y } from "./nodeA11y";
import { addonVersionLabel } from "@/features/addons/version";
import { nodeActionButtonClass, nodeBorderClass, nodeCardClass, nodeNameClass, nodeRailClass } from "./nodeStyle";

export interface AddonNodeData extends CanvasNodeA11y {
  project: string;
  addon: KusoAddon;
  vulns?: VulnSummary;
  __onContext?: (e: React.MouseEvent) => void;
  __onOpenTab?: (tab: string) => void;
}

export function AddonNode({ id, data }: { id: string; data: AddonNodeData }) {
  const status = deriveAddonStatus(data.addon);
  const tone = STATUS_TONE[status.tone];
  const shortName = data.addon.metadata.name.startsWith(data.project + "-")
    ? data.addon.metadata.name.slice(data.project.length + 1)
    : data.addon.metadata.name;
  const meta = [
    addonVersionLabel(addonLabel(data.addon.spec.kind), data.addon.spec.version),
    data.addon.spec.storageSize,
    data.addon.spec.ha ? "HA" : undefined,
  ].filter(Boolean);
  return (
    <div
      data-node-context
      data-canvas-node={id}
      data-focus-key={`addon:${data.addon.metadata.name}`}
      tabIndex={data.__tabIndex ?? -1}
      role="group"
      aria-roledescription="addon"
      aria-label={`${shortName}, ${addonLabel(data.addon.spec.kind)}, ${status.label}`}
      onContextMenu={data.__onContext}
      title={status.detail ? `${status.label}: ${status.detail}` : undefined}
      className={cn(
        // 3 grid units tall (services are 5) so addons read as supporting
        // pieces; both snap to the same 24px grid.
        nodeCardClass,
        "flex h-[72px] w-[220px] flex-col justify-center px-3 py-2",
        nodeBorderClass(status.tone, tone.border),
        nodeFocusClass,
      )}
    >
      <NodeHandles />
      <span aria-hidden className={cn(nodeRailClass, tone.dot)} />
      <button
        tabIndex={-1}
        type="button"
        aria-label={`Actions for ${shortName}`}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          data.__onContext?.(e);
        }}
        className={nodeActionButtonClass}
      >
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>
      <div className="flex items-center gap-2.5 pr-6">
        <AddonIcon kind={data.addon.spec.kind} className="h-5 w-5 shrink-0" />
        <div className="min-w-0 flex-1">
          <p className={cn(nodeNameClass, "flex min-w-0 items-center gap-1.5")}>
            <span className="truncate" title={data.addon.metadata.name}>{shortName}</span>
            <VulnBadge tabIndex={-1} summary={data.vulns} onOpen={() => data.__onOpenTab?.("vulnerabilities")} />
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
