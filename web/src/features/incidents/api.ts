import { api } from "@/lib/api-client";

// Mirrors db.Incident* state constants (server-go/internal/db/incidents.go).
export const INCIDENT_STATES = [
  "investigating",
  "awaiting_feedback",
  "implementing",
  "pr_open",
  "resolved",
  "rejected",
  "dropped",
  "timed_out",
] as const;
export type IncidentState = (typeof INCIDENT_STATES)[number];

export const OPEN_INCIDENT_STATES: readonly IncidentState[] = [
  "investigating",
  "awaiting_feedback",
  "implementing",
  "pr_open",
];

export function isOpenIncident(state: string): boolean {
  return (OPEN_INCIDENT_STATES as readonly string[]).includes(state);
}

// Mirrors db.IncidentFeedback.
export interface IncidentFeedback {
  at: string;
  text?: string;
  decision?: string;
}

// Mirrors db.Incident. contextPack is free-form JSON the trigger captured;
// the UI only pretty-prints it, so it stays unknown.
export interface Incident {
  id: string;
  eventType: string;
  project: string;
  service: string;
  targetKey: string;
  state: string;
  title: string;
  severity: string;
  contextPack: unknown;
  findings: string;
  feedback: IncidentFeedback[] | null;
  discordThread: string;
  prUrl: string;
  prNumber: number;
  investigateJob: string;
  implementJob: string;
  createdAt: string;
  updatedAt: string;
  closedAt?: string;
}

export const INCIDENTS_PAGE_SIZE = 50;

export async function listIncidents(params: {
  state?: string;
  limit?: number;
  offset?: number;
}): Promise<Incident[]> {
  const sp = new URLSearchParams();
  sp.set("limit", String(params.limit ?? INCIDENTS_PAGE_SIZE));
  if (params.offset) sp.set("offset", String(params.offset));
  if (params.state) sp.set("state", params.state);
  const res = await api<{ incidents: Incident[] | null }>(`/api/incidents?${sp.toString()}`);
  return res?.incidents ?? [];
}

export async function getIncident(id: string): Promise<Incident> {
  return api<Incident>(`/api/incidents/${encodeURIComponent(id)}`);
}

// The server routes on decision first: "go" spawns the implement Job,
// "reject" closes the incident, anything else records `text` as a comment.
export type IncidentFeedbackBody = { decision: "go" | "reject" } | { text: string };

export async function sendIncidentFeedback(id: string, body: IncidentFeedbackBody): Promise<void> {
  await api(`/api/incidents/${encodeURIComponent(id)}/feedback`, { method: "POST", body });
}

export async function resolveIncident(id: string): Promise<void> {
  await api(`/api/incidents/${encodeURIComponent(id)}/resolve`, { method: "POST" });
}
