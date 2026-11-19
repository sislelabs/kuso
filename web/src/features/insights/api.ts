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
  services: InsightsService[];
  deploys: InsightsDeploy[];
}

export async function getInsights(range: InsightsRange): Promise<Insights> {
  return api<Insights>(`/api/insights?range=${range}`);
}
