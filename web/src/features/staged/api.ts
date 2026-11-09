import { api } from "@/lib/api-client";
import type { PatchServiceBody } from "@/features/services/api";

export interface ServiceChangesBody {
  envSet?: { name: string; value: string }[];
  envUnset?: string[];
  patch?: PatchServiceBody;
  // Free-form note recorded in the audit log with the change.
  message?: string;
}

export interface ServiceChangeResult {
  kind: "env-set" | "env-unset" | "patch";
  name?: string;
  ok: boolean;
  error?: string;
}

export interface ServiceChangesResult {
  results: ServiceChangeResult[];
  // True when the env CRs were written (one rollout per env).
  rolledOut: boolean;
  flushError?: string;
}

// Applies a service's staged edits in one request; the server writes each
// environment once, so the pods roll once.
export async function applyServiceChanges(
  project: string,
  service: string,
  body: ServiceChangesBody,
): Promise<ServiceChangesResult> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/changes`, {
    method: "POST",
    body,
  });
}
