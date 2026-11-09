import type { NotificationEventType } from "@/features/notifications";
import type { LatestDelivery, NotificationChannel } from "./api";

// Built-in alerts are the events kuso emits on its own (build failed, pod
// crashed, service down…). They have no rule row; a channel's event list
// and project filter are their routing. alert.fired is the event user
// rules fire through, so it isn't listed as a built-in.
export const RULE_EVENT_TYPE = "alert.fired";

export interface AlertRoute {
  channel: string;
  name: string;
  type: string;
  /** Projects the channel is limited to; empty = every project. */
  projects: string[];
}

export interface Routing {
  routes: AlertRoute[];
  /** Disabled channels that subscribe to the event. */
  paused: number;
}

// routingFor mirrors the dispatcher's channelAdmits: an empty event list
// means every event; a project filter admits only its projects, and an
// event with no project (project undefined) reaches every channel.
export function routingFor(type: string, channels: NotificationChannel[], project?: string): Routing {
  const routes: AlertRoute[] = [];
  let paused = 0;
  for (const c of channels) {
    const events = c.events ?? [];
    const projects = c.pipelines ?? [];
    if (events.length > 0 && !events.includes(type)) continue;
    if (project && projects.length > 0 && !projects.includes(project)) continue;
    if (!c.enabled) {
      paused++;
      continue;
    }
    routes.push({ channel: c.id, name: c.name, type: c.type, projects });
  }
  return { routes, paused };
}

export function routeSummary(r: Routing): string {
  if (r.routes.length === 0) {
    if (r.paused === 0) return "Not delivered to any channel";
    return r.paused === 1 ? "Not delivered: its channel is paused" : `Not delivered: its ${r.paused} channels are paused`;
  }
  return r.routes
    .map((x) => (x.projects.length > 0 ? `${x.name} (${x.projects.join(", ")} only)` : x.name))
    .join(", ");
}

export interface BuiltinAlertRow {
  type: string;
  label: string;
  group: NotificationEventType["group"];
  routing: Routing;
  deliveries: LatestDelivery[];
}

export function builtinAlertRows(
  catalogue: NotificationEventType[],
  channels: NotificationChannel[],
  deliveries: LatestDelivery[],
): BuiltinAlertRow[] {
  return catalogue
    .filter((e) => e.type !== RULE_EVENT_TYPE)
    .map((e) => ({
      type: e.type,
      label: e.label,
      group: e.group,
      routing: routingFor(e.type, channels),
      deliveries: deliveries.filter((d) => d.eventType === e.type && !d.ruleId),
    }));
}

export function deliveriesForRule(deliveries: LatestDelivery[], ruleId: string): LatestDelivery[] {
  return deliveries.filter((d) => d.eventType === RULE_EVENT_TYPE && d.ruleId === ruleId);
}

export interface DeliverySummary {
  ok: boolean;
  text: string;
  at: string;
}

// lastDelivery words the newest delivery attempt across channels. A
// channel whose latest delivery failed or is still retrying wins over
// newer successes elsewhere: that's the one someone has to fix.
export function lastDelivery(ds: LatestDelivery[]): DeliverySummary | null {
  if (ds.length === 0) return null;
  const newestFirst = [...ds].sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
  const bad = newestFirst.find((d) => d.status === "failed" || d.status === "retrying");
  if (bad) {
    const verb = bad.status === "failed" ? "failed" : "is retrying";
    return { ok: false, text: `Last delivery to ${bad.channelName} ${verb}${bad.error ? `: ${bad.error}` : ""}`, at: bad.at };
  }
  const d = newestFirst[0];
  if (d.status === "pending") return { ok: true, text: `Queued for ${d.channelName}`, at: d.at };
  return { ok: true, text: `Last delivered to ${d.channelName}`, at: d.at };
}
