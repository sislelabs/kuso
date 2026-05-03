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
  // The helm-operator doesn't populate status.ready on KusoAddon today
  // (it manages status.deployedRelease, which we don't model). Treat
  // "connectionSecret exists" as the ready signal — it's accurate
  // because our addon helm charts emit the secret only after the
  // workload reaches Deployed, and it's what callers actually need
  // (they connect via that secret). Without this, every addon showed
  // a permanent amber pulse even after provisioning succeeded.
  const ready =
    !!data.addon.status?.ready || !!data.addon.status?.connectionSecret;
  return (
    <div
      data-node-context
      onContextMenu={data.__onContext}
      className={cn(
        "w-[220px] rounded-2xl border bg-card p-3 shadow-[var(--shadow-sm)] transition-all",
        "hover:shadow-[var(--shadow-md)]",
        ready ? "border-[var(--border-subtle)]" : "border-amber-500/30 animate-pulse"
      )}
      style={{
        background: "linear-gradient(135deg, var(--card), var(--bg-secondary))",
      }}
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
