import { api } from "@/lib/api-client";

// Mirrors audit.Entry (server-go/internal/audit/audit.go).
export interface ServiceAuditEntry {
  id: number;
  timestamp: string;
  user: string;
  severity: string;
  action: string;
  namespace: string;
  phase: string;
  app: string;
  pipeline: string;
  resource: string;
  message: string;
}

export interface ServiceAuditResponse {
  audit: ServiceAuditEntry[] | null;
  count: number;
  limit: number;
}

// pipeline = project, phase = env-group ("production"), app = short service name.
export async function listServiceAudit(
  project: string,
  phase: string,
  service: string,
  limit = 50,
): Promise<ServiceAuditResponse> {
  return api<ServiceAuditResponse>(
    `/api/audit/app/${encodeURIComponent(project)}/${encodeURIComponent(phase)}/${encodeURIComponent(service)}?limit=${limit}`,
  );
}
