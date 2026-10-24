import { api } from "@/lib/api-client";

// Mirrors projectMetricsResponse (kubernetes_env_metrics.go): CPU + memory
// summed across the project's production env-group. Pods=0 with Envs>0
// means nothing reported (scaled to zero, or metrics-server missing).
export interface ProjectMetrics {
  project: string;
  cpuMillicores: number;
  memBytes: number;
  pods: number;
  envs: number;
}

export async function getProjectMetrics(project: string): Promise<ProjectMetrics> {
  return api<ProjectMetrics>(`/api/projects/${encodeURIComponent(project)}/metrics`);
}

// Mirrors db.LogLine. Project-wide search returns lines from every
// service, so service (the FQ "<project>-<service>" name) is set.
export interface ProjectLogLine {
  id: number;
  ts: string;
  pod: string;
  project?: string;
  service?: string;
  env?: string;
  line: string;
}

export interface ProjectLogSearchResponse {
  project: string;
  service: string;
  q: string;
  lines: ProjectLogLine[] | null;
  nextBeforeId?: number;
}

export async function searchProjectLogs(
  project: string,
  params: { q?: string; env?: string; since?: string; limit?: number; beforeId?: number },
): Promise<ProjectLogSearchResponse> {
  const sp = new URLSearchParams();
  if (params.q) sp.set("q", params.q);
  if (params.env) sp.set("env", params.env);
  if (params.since) sp.set("since", params.since);
  if (params.limit) sp.set("limit", String(params.limit));
  if (params.beforeId) sp.set("beforeId", String(params.beforeId));
  const qs = sp.toString();
  return api<ProjectLogSearchResponse>(
    `/api/projects/${encodeURIComponent(project)}/logs/search${qs ? "?" + qs : ""}`,
  );
}

// "142m" up to 999m, then cores with one decimal ("1.4", "12").
export function formatMillicores(m: number): string {
  if (m < 1000) return `${m}m`;
  const cores = m / 1000;
  return cores >= 10 ? cores.toFixed(0) : cores.toFixed(1).replace(/\.0$/, "");
}

// Binary units to match metrics-server.
export function formatBytes(b: number): string {
  if (b <= 0) return "0 MiB";
  const MiB = 1024 * 1024;
  const GiB = 1024 * MiB;
  if (b >= GiB) {
    const v = b / GiB;
    return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} GiB`;
  }
  return `${Math.round(b / MiB)} MiB`;
}
