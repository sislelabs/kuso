"use client";

import Link from "next/link";
import { Server } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";

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
}

// ServersPopover renders a compact "<n> nodes" pill in the top nav.
// Hovering it (or clicking on touch) drops a popover that lists every
// node grouped by region. Each node shows Ready state, role, and a
// taint badge so the operator can see at a glance which nodes carry
// the kuso.sislelabs.com/region=eu-west tarp. The full editor lives at
// /settings/nodes — this popover is the read-mostly summary.
export function ServersPopover() {
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    refetchInterval: 30_000,
    staleTime: 15_000,
  });

  const list = nodes.data ?? [];
  const ready = list.filter((n) => n.ready).length;
  const total = list.length;

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

  return (
    <Popover>
      <PopoverTrigger
        aria-label="Cluster nodes"
        className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-xs font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)]"
      >
        <Server className="h-3.5 w-3.5" />
        <span className="font-mono">
          {ready}/{total} nodes
        </span>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
          <p className="text-xs font-semibold tracking-tight">Cluster nodes</p>
          <Link
            href="/settings/nodes"
            className="font-mono text-[10px] text-[var(--accent)] hover:underline"
          >
            manage →
          </Link>
        </header>
        <div className="max-h-72 overflow-y-auto">
          {nodes.isPending ? (
            <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">Loading…</p>
          ) : nodes.isError ? (
            <p className="px-3 py-4 text-xs text-red-400">
              Failed to load nodes: {nodes.error?.message}
            </p>
          ) : list.length === 0 ? (
            <p className="px-3 py-4 text-xs text-[var(--text-tertiary)]">
              No nodes returned. Check that the kuso server has cluster-read RBAC.
            </p>
          ) : (
            <ul className="divide-y divide-[var(--border-subtle)]">
              {regions.map(([region, ns]) => (
                <li key={region}>
                  <div className="px-3 py-1.5 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    {region}
                  </div>
                  <ul>
                    {ns.map((n) => (
                      <li
                        key={n.name}
                        className="flex items-center gap-2 px-3 py-1.5 text-xs"
                      >
                        <span
                          className={cn(
                            "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                            n.ready ? "bg-emerald-400" : "bg-red-400"
                          )}
                        />
                        <span className="truncate font-mono">{n.name}</span>
                        <span className="ml-auto flex shrink-0 items-center gap-1 text-[10px] text-[var(--text-tertiary)]">
                          {n.roles.map((r) => (
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
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          )}
        </div>
        <footer className="border-t border-[var(--border-subtle)] px-3 py-2 text-[10px] text-[var(--text-tertiary)]">
          Add a <span className="font-mono text-[var(--text-secondary)]">region</span> label to
          group nodes; projects pin to a region with their own label.
        </footer>
      </PopoverContent>
    </Popover>
  );
}
