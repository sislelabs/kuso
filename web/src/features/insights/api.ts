import { api } from "@/lib/api-client";

export type InsightsRange = "1h" | "6h" | "24h";

/** [unix seconds, value] */
export type InsightsPoint = [number, number];

export interface InsightsService {
  project: string;
  service: string;
  env: string;
  /** requests per minute */
  requests: InsightsPoint[];
  /** millicores */
  cpu: InsightsPoint[];
  /** bytes */
  memory: InsightsPoint[];
  /** milliseconds */
  p95: InsightsPoint[];
  /** percent of requests answered 5xx */
  errorRate: InsightsPoint[];
  /** replicas at the latest usage sample */
  pods: number;
  /** one pod's requests and limits; absent when the service sets none */
  resources?: InsightsResources;
}

export interface InsightsResources {
  cpuRequestMilli?: number;
  cpuLimitMilli?: number;
  memRequestBytes?: number;
  memLimitBytes?: number;
}

export interface InsightsFailedBuild {
  project: string;
  service: string;
  at: string;
  status: string;
  commitMessage: string;
  error: string;
}

export interface InsightsCertProblem {
  project: string;
  host: string;
  problem: string;
}

export interface InsightsDeploy {
  project: string;
  service: string;
  at: string;
  commitSha: string;
  commitMessage: string;
}

export interface Insights {
  range: InsightsRange;
  stepSeconds: number;
  since: string;
  until: string;
  requestsAvailable: boolean;
  usageAvailable: boolean;
  /** Oldest CPU/memory sample: the history is in memory and starts at the server's last restart. */
  usageSince?: string;
  services: InsightsService[];
  deploys: InsightsDeploy[];
  failedBuilds: InsightsFailedBuild[];
  certProblems: InsightsCertProblem[];
}

export async function getInsights(range: InsightsRange): Promise<Insights> {
  return api<Insights>(`/api/insights?range=${range}`);
}
