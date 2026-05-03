"use client";

import { Handle, Position } from "@xyflow/react";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import type { KusoAddon } from "@/types/projects";
import { cn } from "@/lib/utils";

export interface AddonNodeData extends Record<string, unknown> {
  project: string;
  addon: KusoAddon;
  __onContext?: (e: React.MouseEvent) => void;
}

export function AddonNode({ data }: { data: AddonNodeData }) {
  // helm-operator owns .status on every CR it manages and it doesn't
  // populate a custom .ready field — only .conditions[] and
  // .deployedRelease. The right signal is therefore
  //   conditions[?(@.type=="Deployed" && @.status=="True")]
  // which flips True the moment the helm release reaches Deployed.
  // Earlier code looked at .status.ready / .status.connectionSecret;
  // both are nil on the live CR so addons always pulsed amber.
  const conditions = (data.addon.status?.conditions ?? []) as Array<{ type?: string; status?: string }>;
  const ready =
    !!data.addon.status?.ready ||
    !!data.addon.status?.connectionSecret ||
    conditions.some((c) => c.type === "Deployed" && c.status === "True");
  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        "w-[220px] rounded-2xl border bg-[var(--bg-elevated)] p-3 transition-colors",
        "hover:border-[var(--border-strong)]",
        ready ? "border-[var(--border-subtle)]" : "border-amber-500/30 animate-pulse"
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />
      <div className="flex items-center gap-2">
        <AddonIcon kind={data.addon.spec.kind} className="h-5 w-5" />
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{data.addon.metadata.name}</p>
          <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {addonLabel(data.addon.spec.kind)}
            {data.addon.spec.version ? ` · ${data.addon.spec.version}` : ""}
          </p>
        </div>
      </div>
      {data.addon.status?.connectionSecret && (
        <p className="mt-2 truncate font-mono text-[9px] text-[var(--text-tertiary)]">
          secret: {data.addon.status.connectionSecret}
        </p>
      )}
    </div>
  );
}
