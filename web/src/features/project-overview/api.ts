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

export async function getProjectMetrics(project: string, env = "production"): Promise<ProjectMetrics> {
  return api<ProjectMetrics>(
    `/api/projects/${encodeURIComponent(project)}/metrics?env=${encodeURIComponent(env)}`,
  );
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
