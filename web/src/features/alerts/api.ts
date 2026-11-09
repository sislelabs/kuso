import { api } from "@/lib/api-client";

export type AlertKind =
  | "log_match"
  | "node_cpu"
  | "node_mem"
  | "node_disk"
  | "http_5xx_rate"
  | "http_p95_latency"
  | "cert_expiry"
  | "dns_mismatch"
  | "crashloop"
  | "deploy_failed"
  | "healthcheck_failing"
  | "rollout_failing";

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
  // Set while an episodic rule (every kind but log_match / node_*) is firing.
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

// updateAlert rewrites a rule's definition (PUT; enabled is unchanged).
export async function updateAlert(id: string, body: CreateAlertBody): Promise<AlertRule> {
  return api(`/api/alerts/${encodeURIComponent(id)}`, { method: "PUT", body });
}

// One channel's result for a test send. error is a category ("timeout",
// "receiver returned HTTP 401"), never the channel's URL.
export interface AlertTestDelivery {
  channel: string;
  name: string;
  type: string;
  ok: boolean;
  error?: string;
}

export interface AlertTestResult {
  results: AlertTestDelivery[];
  // The project is muted: real non-error firings wouldn't be delivered.
  muted?: boolean;
}

export async function testAlert(id: string): Promise<AlertTestResult> {
  return api(`/api/alerts/${encodeURIComponent(id)}/test`, { method: "POST" });
}

// testEventType sends a built-in event (build.failed, pod.crashed, …),
// marked as a test, through the routing a real one would get.
export async function testEventType(type: string, project?: string): Promise<AlertTestResult> {
  return api(`/api/notifications/event-types/${encodeURIComponent(type)}/test`, {
    method: "POST",
    body: project ? { project } : {},
  });
}

// LatestDelivery is the newest delivery of one event type (or one alert
// rule, for alert.fired) to one channel, read from the delivery queue.
export interface LatestDelivery {
  eventType: string;
  ruleId?: string;
  channel: string;
  channelName: string;
  channelType: string;
  status: "delivered" | "pending" | "retrying" | "failed";
  error?: string;
  at: string;
}

export async function listLatestDeliveries(): Promise<LatestDelivery[]> {
  return api("/api/notifications/deliveries/latest");
}

// NotificationChannel is the part of a Settings → Notifications channel
// the alerts page reads to show where a built-in alert goes.
export interface NotificationChannel {
  id: string;
  name: string;
  type: string;
  enabled: boolean;
  pipelines: string[];
  events: string[];
}

// Shares the ["admin", "notifications"] cache entry with the notifications
// page, so it unwraps the envelope to the same array.
export async function listNotificationChannels(): Promise<NotificationChannel[]> {
  const res = await api<{ data?: NotificationChannel[] } | NotificationChannel[]>("/api/notifications");
  return Array.isArray(res) ? res : (res.data ?? []);
}
