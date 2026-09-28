import { api } from "@/lib/api-client";

export type NotificationEventGroup =
  | "build"
  | "runtime"
  | "jobs"
  | "nodes"
  | "backups"
  | "other";

// NotificationEventType mirrors one row of GET /api/notifications/event-types,
// the server's catalogue of every event a channel can subscribe to.
// defaultMention is "@here" or "" — what a Discord channel pings with when
// no per-event mention rule is set.
export interface NotificationEventType {
  type: string;
  label: string;
  group: NotificationEventGroup;
  defaultMention: string;
}

export async function listNotificationEventTypes(): Promise<NotificationEventType[]> {
  return api<NotificationEventType[]>("/api/notifications/event-types");
}
