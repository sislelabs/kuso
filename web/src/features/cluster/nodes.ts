"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Perms, useCan } from "@/features/auth";
import type { NodeCondition } from "@/components/layout/nodeHealth";

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

export const nodesQueryKey = ["kubernetes", "nodes"] as const;

// useNodes reads the cluster node list, which the server serves to
// instance admins only. For anyone else the request is never sent and
// canRead is false: callers must treat the list as unknown, not empty.
export function useNodes({ enabled = true, poll = false }: { enabled?: boolean; poll?: boolean } = {}) {
  const canRead = useCan(Perms.SettingsAdmin);
  const query = useQuery({
    queryKey: nodesQueryKey,
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    enabled: canRead && enabled,
    staleTime: 15_000,
    refetchInterval: poll ? 30_000 : false,
  });
  return { ...query, canRead };
}
