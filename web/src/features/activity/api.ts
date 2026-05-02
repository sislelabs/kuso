import { api } from "@/lib/api-client";

export interface AuditEntry {
  id: string;
  timestamp?: string;
  user?: string;
  action?: string;
  pipelineName?: string;
  phaseName?: string;
  appName?: string;
  message?: string;
  severity?: string;
  // The Go server returns a slim shape; keep the rest passable as unknown
  // until we extend the endpoint with project filter (Phase E).
  [k: string]: unknown;
}

export interface AuditResponse {
  audit: AuditEntry[];
  count: number;
  limit: number;
}

export async function listAudit(limit = 100): Promise<AuditResponse> {
  return api(`/api/audit?limit=${limit}`);
}
