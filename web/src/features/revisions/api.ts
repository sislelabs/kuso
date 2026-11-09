import { api } from "@/lib/api-client";

export type RevisionKind = "service" | "addon" | "environment" | "cron";

// Revision mirrors server-go db.Revision. snapshot is the stored
// mutation payload; for viewers without secrets:read it arrives redacted.
export interface Revision {
  id: string;
  project: string;
  kind: RevisionKind;
  name: string;
  actor?: string;
  summary?: string;
  snapshot: unknown;
  createdAt: string;
  // Key names a secret write touched (never values). Absent on servers
  // older than this field.
  changedKeys?: string[];
}

export async function listRevisions(
  project: string,
  kind: RevisionKind,
  name: string,
  limit = 50,
): Promise<Revision[]> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/revisions/${encodeURIComponent(kind)}/${encodeURIComponent(name)}?limit=${limit}`,
  );
}

// revertRevision replays a revision. env scopes an image rollback to that
// environment (the server answers kind "environment"); servers without
// env scoping ignore it and replay service-wide (kind "service").
export async function revertRevision(
  project: string,
  id: string,
  env?: string,
): Promise<{ status: string; kind: string }> {
  const qs = env ? `?env=${encodeURIComponent(env)}` : "";
  return api(
    `/api/projects/${encodeURIComponent(project)}/revisions/${encodeURIComponent(id)}/revert${qs}`,
    { method: "POST" },
  );
}
