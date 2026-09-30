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

export async function revertRevision(
  project: string,
  id: string,
): Promise<{ status: string; kind: string }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/revisions/${encodeURIComponent(id)}/revert`,
    { method: "POST" },
  );
}
