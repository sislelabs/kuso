import { api } from "@/lib/api-client";

// Mirrors uptime.ServiceStatus (server-go/internal/uptime/status.go).
export type UptimeState = "up" | "failing" | "down" | "paused" | "disabled" | "pending";

export interface UptimeServiceStatus {
  // Short service name (project prefix stripped server-side).
  service: string;
  env: string;
  state: UptimeState | string;
  reason?: string;
  since?: string;
  lastCheckedAt?: string;
  latencyMs?: number;
  statusCode?: number;
  error?: string;
  url: string;
  // Last check is old: state is last-known, not current.
  stale?: boolean;
}

export interface ProjectUptime {
  // false = the checker is switched off instance-wide.
  enabled: boolean;
  services: UptimeServiceStatus[] | null;
}

export async function getProjectUptime(project: string): Promise<ProjectUptime> {
  return api<ProjectUptime>(`/api/projects/${encodeURIComponent(project)}/uptime`);
}

export interface UptimeSummary {
  up: number;
  failing: number;
  down: number;
  other: number;
}

export function summarizeUptime(services: UptimeServiceStatus[]): UptimeSummary {
  const out: UptimeSummary = { up: 0, failing: 0, down: 0, other: 0 };
  for (const s of services) {
    if (s.state === "up") out.up++;
    else if (s.state === "failing") out.failing++;
    else if (s.state === "down") out.down++;
    else out.other++;
  }
  return out;
}
