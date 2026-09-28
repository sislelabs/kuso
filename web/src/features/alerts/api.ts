import { api } from "@/lib/api-client";

export type AlertKind =
  | "log_match"
  | "node_cpu"
  | "node_mem"
  | "node_disk"
  | "http_5xx_rate"
  | "http_p95_latency"
  | "cert_expiry"
  | "dns_mismatch";

export interface AlertRule {
  id: string;
  name: string;
  enabled: boolean;
  kind: AlertKind;
  project?: string;
  service?: string;
  env?: string;
  query?: string;
  thresholdInt?: number;
  thresholdFloat?: number;
  windowSeconds: number;
  severity: "info" | "warn" | "error";
  throttleSeconds: number;
  lastFiredAt?: string;
  // Set while an episodic rule (http_*, cert_expiry, dns_mismatch) is firing.
  firingSince?: string;
  firingTargets?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface CreateAlertBody {
  name: string;
  kind: AlertKind;
  project?: string;
  service?: string;
  env?: string;
  query?: string;
  thresholdInt?: number;
  thresholdFloat?: number;
  windowSeconds?: number;
  severity?: AlertRule["severity"];
  throttleSeconds?: number;
}

export async function listAlerts(): Promise<AlertRule[]> {
  return api("/api/alerts");
}

export async function createAlert(body: CreateAlertBody): Promise<AlertRule> {
  return api("/api/alerts", { method: "POST", body });
}

export async function deleteAlert(id: string): Promise<void> {
  return api(`/api/alerts/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function enableAlert(id: string): Promise<void> {
  return api(`/api/alerts/${encodeURIComponent(id)}/enable`, { method: "POST" });
}

export async function disableAlert(id: string): Promise<void> {
  return api(`/api/alerts/${encodeURIComponent(id)}/disable`, { method: "POST" });
}
