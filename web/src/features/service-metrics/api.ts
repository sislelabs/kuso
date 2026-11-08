import { api } from "@/lib/api-client";

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
