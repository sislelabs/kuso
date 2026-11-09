"use client";

import Link from "next/link";
import { Server } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { LoadingState } from "@/components/ui/loading-state";
import { api } from "@/lib/api-client";
import { useCan, Perms } from "@/features/auth";
import { cn } from "@/lib/utils";
import { nodePressures, roleLabels, type NodeCondition } from "./nodeHealth";

export interface NodeSummary {
  name: string;
  ready: boolean;
  roles: string[];
  region?: string;
  zone?: string;
  // kusoLabels mirrors the kuso.sislelabs.com/* labels with the
  // namespace prefix stripped. Only this set is editable through the
  // UI; underlying kube labels stay invisible.
  kusoLabels: Record<string, string>;
  schedulable: boolean;
  unreachable?: boolean;
  createdAt?: string;
  // Live capacity + usage. cpu in milli-cores, memory + disk in
  // bytes. Usage fields are 0 when metrics-server isn't installed —
  // the UI falls back to "—" in that case.
  cpuCapacityMilli?: number;
  cpuUsageMilli?: number;
  memCapacityBytes?: number;
  memUsageBytes?: number;
  diskCapacityBytes?: number;
  diskAvailableBytes?: number;
  pods?: number;
  podsCapacity?: number;
  // Kubelet conditions (Memory/Disk/PID pressure). Absent on older servers.
  conditions?: NodeCondition[];
}

// ServersPopover renders a compact "<n> nodes" pill in the top nav.
// Hovering it (or clicking on touch) drops a popover that lists every
// node grouped by region. Each node shows Ready state, role, and a
// taint badge so the operator can see at a glance which nodes carry
// the kuso.sislelabs.com/region=eu-west tarp. The full editor lives at
// /settings/nodes — this popover is the read-mostly summary.
const MAX_BARS = 10;

export function ServersPopover() {
  // Node data is admin-only (server: requireAdmin → settings:admin). For
  // a regular viewer the fetch 403s and the pill degrades to "0/0 nodes".
  // Hide the whole thing for non-admins and don't fire the doomed request.
  const isAdmin = useCan(Perms.SettingsAdmin);
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    refetchInterval: 30_000,
    staleTime: 15_000,
    enabled: isAdmin,
  });

  const list = nodes.data ?? [];
  const ready = list.filter((n) => n.ready).length;
  const total = list.length;
  const pressured = list.filter((n) => nodePressures(n).length > 0).length;

  // Group by region (or "default" when unset). Region comes from the
  // upstream topology label OR our kuso/region label, whichever is
  // present (Nodes() projects the union into n.region for us).
  const grouped = new Map<string, NodeSummary[]>();
  for (const n of list) {
    const region = n.region || n.kusoLabels?.region || "default";
    const arr = grouped.get(region) ?? [];
    arr.push(n);
    grouped.set(region, arr);
  }
  const regions = [...grouped.entries()].sort(([a], [b]) => a.localeCompare(b));

  // Non-admins never see the nodes pill at all.
  if (!isAdmin) return null;

  return (
    <Popover>
      <PopoverTrigger
        aria-label={`Cluster nodes: ${ready} of ${total} ready${pressured ? `, ${pressured} under resource pressure` : ""}`}
        title="Cluster nodes"
        className="inline-flex h-7 items-center gap-2 rounded-md px-2 text-xs font-medium whitespace-nowrap text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-elevated)] data-[popup-open]:shadow-[var(--shadow-sm)]"
      >
        {/* One bar per node, so a sick node is visible without opening this. */}
        <span aria-hidden className="flex h-3.5 items-end gap-[2px]">
          {total === 0 ? (
            <Server className="size-3.5" />
          ) : (
            list.slice(0, MAX_BARS).map((n) => (
              <span
                key={n.name}
                className={cn(
                  "w-[3px] rounded-[1px]",
                  !n.ready ? "h-1.5 bg-[var(--error)]" : nodePressures(n).length > 0 ? "h-2.5 bg-[var(--warning)]" : "h-3.5 bg-[var(--success)]",
                )}
              />
            ))
          )}
        </span>
        <span className="tabular-nums">
          {ready}/{total}
          <span className="hidden md:inline"> nodes</span>
        </span>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
          <p className="text-sm font-semibold tracking-tight">Cluster nodes</p>
          <Link
            href="/settings/nodes"
            className="inline-flex h-7 items-center rounded-md px-2 text-xs text-[var(--link)] hover:bg-[var(--bg-tertiary)]"
          >
            Manage nodes
          </Link>
        </header>
        <div className="max-h-72 overflow-y-auto">
          {nodes.isPending ? (
            <LoadingState kind="inline" className="px-3 py-4" />
          ) : nodes.isError ? (
            <p role="alert" className="px-3 py-4 text-sm text-[var(--error)]">
              Failed to load nodes: {nodes.error?.message}
            </p>
          ) : list.length === 0 ? (
            <p className="px-3 py-4 text-sm text-[var(--text-tertiary)]">
              No nodes returned. Check that the kuso server has cluster-read RBAC.
            </p>
          ) : (
            <ul className="divide-y divide-[var(--border-subtle)]">
              {regions.map(([region, ns]) => (
                <li key={region}>
                  <div className="px-3 pt-2 pb-1 text-xs font-medium text-[var(--text-tertiary)]">
                    {region}
                  </div>
                  <ul>
                    {ns.map((n) => {
                      const pressure = nodePressures(n);
                      return (
                      <li
                        key={n.name}
                        className="flex items-center gap-2 px-3 py-1.5 text-[13px]"
                      >
                        <span
                          className={cn(
                            "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                            !n.ready ? "bg-[var(--error)]" : pressure.length > 0 ? "bg-[var(--warning)]" : "bg-[var(--success)]"
                          )}
                        />
                        <span className="truncate font-mono">{n.name}</span>
                        {pressure.length > 0 && (
                          <span
                            className="shrink-0 text-xs text-[var(--warning)]"
                            title={pressure.map((p) => p.detail).join("\n")}
                          >
                            {pressure.map((p) => p.label).join(", ")}
                          </span>
                        )}
                        <span className="ml-auto flex shrink-0 items-center gap-1 text-2xs text-[var(--text-tertiary)]">
                          {roleLabels(n.roles).map((r) => (
                            <span
                              key={r}
                              className="rounded bg-[var(--bg-tertiary)] px-1 py-0.5"
                            >
                              {r}
                            </span>
                          ))}
                          {Object.keys(n.kusoLabels ?? {}).length > 0 && (
                            <span
                              title={Object.entries(n.kusoLabels)
                                .map(([k, v]) => `${k}=${v}`)
                                .join("\n")}
                              className="rounded bg-[var(--accent-subtle)] px-1 py-0.5 text-[var(--text-secondary)]"
                            >
                              {Object.keys(n.kusoLabels).length} label
                              {Object.keys(n.kusoLabels).length === 1 ? "" : "s"}
                            </span>
                          )}
                        </span>
                      </li>
                      );
                    })}
                  </ul>
                </li>
              ))}
            </ul>
          )}
        </div>
        <footer className="border-t border-[var(--border-subtle)] px-3 py-2 text-xs text-[var(--text-tertiary)]">
          Add a <span className="font-mono text-[var(--text-secondary)]">region</span> label to
          group nodes; projects pin to a region with their own label.
        </footer>
      </PopoverContent>
    </Popover>
  );
}
