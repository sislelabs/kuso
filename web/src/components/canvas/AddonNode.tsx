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
        // Half-height of service nodes (3 × 24px grid units, vs the
        // service's 5). Addons have less to show — kind, version,
        // optional secret name — so we let the canvas breathe by
        // keeping them compact. Both still snap to the same grid so
        // their tops align at the same y.
        // border-2 (vs border-1) so the status color (green/amber)
        // is unambiguously visible at canvas zoom — at 1px the
        // ready/pending state was barely distinguishable from the
        // surface lift.
        "flex h-[72px] w-[220px] flex-col justify-center rounded-2xl border-2 bg-[var(--bg-elevated)] px-3 py-2 transition-colors cursor-pointer",
        // Hover wins over the green ready-border so the user gets a
        // clear "you're targeting this" affordance. Without the
        // explicit hover-on-ready rule the green stays put and the
        // hover only nudges the alpha.
        ready
          ? "border-emerald-500/60 hover:border-[var(--border-strong)]"
          : "border-amber-500/60 animate-pulse hover:border-[var(--border-strong)]"
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-[var(--accent)]" />
      <Handle type="source" position={Position.Right} className="!bg-[var(--accent)]" />
      <div className="flex items-center gap-2">
        <AddonIcon kind={data.addon.spec.kind} className="h-5 w-5" />
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">{data.addon.metadata.name}</p>
          <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
            {/* Three pieces of glance info packed onto one line:
                 - kind + version  (Postgres · 16)
                 - storage size    (10Gi)
                 - HA              (ha) — only when on
                Keeps the half-height node informative without
                adding any server-side scraping. */}
            {addonLabel(data.addon.spec.kind)}
            {data.addon.spec.version ? ` · ${data.addon.spec.version}` : ""}
            {data.addon.spec.storageSize ? ` · ${data.addon.spec.storageSize}` : ""}
            {data.addon.spec.ha ? " · ha" : ""}
          </p>
        </div>
      </div>
      {/* connectionSecret name was a separate line here; we removed
          it to halve the node height. The user can still see the
          secret name on the addon overlay panel + via `kuso get
          addons`, so the canvas summary stays glanceable. */}
    </div>
  );
}
