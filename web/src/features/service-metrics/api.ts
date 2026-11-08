import { ApiError, api } from "@/lib/api-client";
import type { ResourceSample } from "./history";

export interface PodMetric {
  pod: string;
  timestamp?: string;
  cpuMillicores: number;
  memBytes: number;
}

export interface EnvMetricsResponse {
  env: string;
  window?: string;
  pods: PodMetric[];
}

// Points are [unixSeconds, value].
export interface TimeseriesResponse {
  env: string;
  range: string;
  step: string;
  series: {
    requests?: [number, number][];
    errors?: [number, number][];
    p95ms?: [number, number][];
  };
}

export function getEnvPodMetrics(env: string): Promise<EnvMetricsResponse> {
  return api<EnvMetricsResponse>(`/api/kubernetes/envs/${encodeURIComponent(env)}/metrics`);
}

export function getEnvTimeseries(env: string, range: string): Promise<TimeseriesResponse> {
  return api<TimeseriesResponse>(`/api/kubernetes/envs/${encodeURIComponent(env)}/timeseries?range=${encodeURIComponent(range)}`);
}

// Server-kept CPU/memory history (in memory, up to 24h). Since is the
// oldest sample the server holds, null when it has none.
export interface UsageHistoryResponse {
  stepSeconds: number;
  samples: ResourceSample[];
  since: number | null;
}

// Resolves null when the server predates the endpoint (404), so the
// caller can fall back to its own polling.
export async function getServiceUsageHistory(
  project: string,
  service: string,
  env: string,
  range: string,
): Promise<UsageHistoryResponse | null> {
  const q = new URLSearchParams({ env, range });
  try {
    return await api<UsageHistoryResponse>(
      `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/usage/history?${q}`,
    );
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

// Parses Go-style step strings ("30s", "2m", "1h") to ms.
export function stepToMs(step: string): number {
  const m = /^(\d+)(s|m|h)$/.exec(step);
  if (!m) return 0;
  return parseInt(m[1], 10) * { s: 1000, m: 60_000, h: 3_600_000 }[m[2] as "s" | "m" | "h"];
}

// parseCpuQuantity turns a k8s CPU quantity ("500m", "1", "1.5") into millicores.
export function parseCpuQuantity(q: string | undefined): number | undefined {
  if (!q) return undefined;
  const m = /^(\d+(?:\.\d+)?)(m?)$/.exec(q.trim());
  if (!m) return undefined;
  const n = parseFloat(m[1]);
  return m[2] === "m" ? n : n * 1000;
}
