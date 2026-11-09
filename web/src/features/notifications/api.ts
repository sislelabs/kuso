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

// ProjectFeedEvent is one row of the notification feed (server
// db.NotificationEvent), trimmed to what project views read.
export interface ProjectFeedEvent {
  id: number;
  type: string;
  title: string;
  body?: string;
  severity?: string;
  project?: string;
  service?: string;
  url?: string;
  createdAt: string;
  classification?: { kind?: string; summary?: string };
}

// listProjectNotifications reads one project's newest events. The server
// filters in SQL (?project=) and hides events for deleted services; the
// client-side filter keeps older servers, which ignore the parameter and
// return the global feed, from leaking other projects' rows.
export async function listProjectNotifications(project: string, limit = 30): Promise<ProjectFeedEvent[]> {
  const rows = await api<ProjectFeedEvent[]>(
    `/api/notifications/my-feed?project=${encodeURIComponent(project)}&limit=${limit}`,
  );
  return (rows ?? []).filter((e) => e.project === project);
}
