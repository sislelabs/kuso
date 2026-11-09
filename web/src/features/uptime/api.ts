import { api } from "@/lib/api-client";
import { formatLatency } from "@/lib/format";

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
  // Paused, disabled or not checked yet: not counted in `checked`.
  other: number;
  // Checks that are enabled and have a result: the "N/M up" denominator.
  checked: number;
}

export function summarizeUptime(services: UptimeServiceStatus[]): UptimeSummary {
  const out: UptimeSummary = { up: 0, failing: 0, down: 0, other: 0, checked: 0 };
  for (const s of services) {
    if (s.state === "up") out.up++;
    else if (s.state === "failing") out.failing++;
    else if (s.state === "down") out.down++;
    else out.other++;
  }
  out.checked = out.up + out.failing + out.down;
  return out;
}

export type UptimeViewState = "up" | "failing" | "down" | "paused" | "not_set_up" | "unknown";

// One reading of a service's uptime ping (not the pod health-check probe), shared by the overlay strip,
// the Deployments badge and the canvas summary.
export interface UptimeView {
  state: UptimeViewState;
  latencyMs?: number;
  // "OK · 4 ms", "Failing", "Down", "Paused", "Not set up", …
  label: string;
  stale: boolean;
}

export function uptimeView(s: UptimeServiceStatus | undefined): UptimeView {
  if (!s) return { state: "not_set_up", label: "Not set up", stale: false };
  const stale = !!s.stale;
  const suffix = "";
  switch (s.state) {
    case "up":
      return {
        state: "up",
        latencyMs: s.latencyMs,
        label: `OK${s.latencyMs ? ` · ${formatLatency(s.latencyMs)}` : ""}${suffix}`,
        stale,
      };
    case "failing":
      return { state: "failing", label: `Failing${suffix}`, stale };
    case "down":
      return { state: "down", label: `Down${suffix}`, stale };
    case "paused":
      return { state: "paused", label: "Paused", stale };
    case "disabled":
      return { state: "paused", label: "Off", stale };
    case "pending":
      return { state: "unknown", label: "Waiting for first check", stale };
    default:
      return { state: "unknown", label: "Unknown", stale };
  }
}
