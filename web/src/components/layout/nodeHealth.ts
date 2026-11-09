// Node pressure + role helpers shared by the top-nav nodes pill and the
// Nodes settings page. `conditions` mirrors the kubelet's node conditions;
// older servers don't send it, so every helper treats it as optional.

export interface NodeCondition {
  type: string;
  status: string;
  reason?: string;
  message?: string;
}

export interface NodePressure {
  type: "MemoryPressure" | "DiskPressure" | "PIDPressure";
  label: string;
  detail: string;
  message?: string;
}

const PRESSURE: Record<NodePressure["type"], { label: string; detail: string }> = {
  MemoryPressure: {
    label: "Low on memory",
    detail: "The node is running out of memory. Kubernetes may evict pods and won't schedule new ones here.",
  },
  DiskPressure: {
    label: "Low on disk",
    detail: "The node is running out of disk space. New pods can't be scheduled here and images may be cleaned up.",
  },
  PIDPressure: {
    label: "Too many processes",
    detail: "The node is running out of process IDs. New pods can't be scheduled here.",
  },
};

export function nodePressures(node: { conditions?: ReadonlyArray<NodeCondition> | null }): NodePressure[] {
  const out: NodePressure[] = [];
  for (const c of node.conditions ?? []) {
    if (c.status !== "True" || !(c.type in PRESSURE)) continue;
    const type = c.type as NodePressure["type"];
    out.push({ type, ...PRESSURE[type], message: c.message || undefined });
  }
  return out;
}

export function roleLabel(role: string): string {
  if (role === "control-plane" || role === "master") return "Control plane";
  if (role === "worker") return "Worker";
  return role.charAt(0).toUpperCase() + role.slice(1).replace(/-/g, " ");
}

// k3s stamps a server with both control-plane and master (and often etcd);
// show each human label once.
export function roleLabels(roles: ReadonlyArray<string> | null | undefined): string[] {
  return [...new Set((roles ?? []).map(roleLabel))];
}
