import { api } from "@/lib/api-client";

// The subset of a core/v1 Event the events view renders. The server
// returns the raw kube objects (newest first, at most 200).
export interface KubeEvent {
  metadata: { name: string; namespace?: string; uid?: string };
  involvedObject?: { kind?: string; name?: string; namespace?: string };
  reason?: string;
  message?: string;
  type?: string;
  count?: number;
  firstTimestamp?: string | null;
  lastTimestamp?: string | null;
  eventTime?: string | null;
  source?: { component?: string; host?: string };
  reportingComponent?: string;
}

export async function listKubeEvents(namespace: string): Promise<KubeEvent[]> {
  const qs = namespace ? `?namespace=${encodeURIComponent(namespace)}` : "";
  const res = await api<KubeEvent[] | null>(`/api/kubernetes/events${qs}`);
  return res ?? [];
}

// Newest timestamp kube recorded for the event. lastTimestamp is unset on
// events.k8s.io-style events, which carry eventTime instead.
export function eventTime(e: KubeEvent): string | undefined {
  return e.lastTimestamp || e.eventTime || e.firstTimestamp || undefined;
}
