import { api } from "@/lib/api-client";
import type {
  KusoAddon,
  KusoEnvironment,
  KusoProject,
  KusoService,
} from "@/types/projects";

export async function listProjects(): Promise<KusoProject[]> {
  return api<KusoProject[]>("/api/projects");
}

export async function getProject(name: string): Promise<{
  project: KusoProject;
  services: KusoService[];
  environments: KusoEnvironment[];
}> {
  return api(`/api/projects/${encodeURIComponent(name)}`);
}

export async function listServices(project: string): Promise<KusoService[]> {
  return api<KusoService[]>(`/api/projects/${encodeURIComponent(project)}/services`);
}

export async function listEnvironments(
  project: string
): Promise<KusoEnvironment[]> {
  return api<KusoEnvironment[]>(`/api/projects/${encodeURIComponent(project)}/envs`);
}

export async function createEnvironment(
  project: string,
  service: string,
  body: { name: string; branch: string; host?: string }
): Promise<KusoEnvironment> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/envs`,
    { method: "POST", body }
  );
}

export async function listAddons(project: string): Promise<KusoAddon[]> {
  return api<KusoAddon[]>(`/api/projects/${encodeURIComponent(project)}/addons`);
}

export async function addAddon(
  project: string,
  body: { name: string; kind: string }
): Promise<KusoAddon> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons`,
    { method: "POST", body }
  );
}

export async function deleteAddon(project: string, addon: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}`,
    { method: "DELETE" }
  );
}

// addonSecret returns the addon's connection secret as plaintext
// key→value pairs. Used by the overview panel so the user can copy
// DATABASE_URL / POSTGRES_PASSWORD / etc. and connect from local
// tools. Server gates this behind secrets:read.
export async function addonSecret(
  project: string,
  addon: string
): Promise<{ values: Record<string, string> }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/secret`
  );
}

export interface BackupObject {
  key: string;
  size: number;
  when: string;
}

export async function listBackups(project: string, addon: string): Promise<BackupObject[]> {
  return api<BackupObject[]>(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/backups`
  );
}

export async function restoreBackup(
  project: string,
  addon: string,
  key: string
): Promise<{ job: string }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/backups/restore`,
    { method: "POST", body: { key } }
  );
}

export interface SQLTable {
  schema: string;
  name: string;
}
export async function listSQLTables(project: string, addon: string): Promise<SQLTable[]> {
  return api<SQLTable[]>(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/sql/tables`
  );
}

export interface SQLQueryResponse {
  columns: string[];
  rows: string[][];
  truncated: boolean;
  elapsed: string;
}
export async function runSQL(
  project: string,
  addon: string,
  query: string,
  limit?: number
): Promise<SQLQueryResponse> {
  return api<SQLQueryResponse>(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/sql/query`,
    { method: "POST", body: { query, limit } }
  );
}
