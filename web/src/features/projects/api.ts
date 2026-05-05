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
  body: {
    name: string;
    kind: string;
    external?: { secretName: string; secretKeys?: string[] };
    useInstanceAddon?: string;
  }
): Promise<KusoAddon> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons`,
    { method: "POST", body }
  );
}

// resyncExternalAddon re-mirrors the upstream Secret. Use after
// rotating credentials on the managed datastore side.
export async function resyncExternalAddon(
  project: string,
  addon: string
): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/resync-external`,
    { method: "POST" }
  );
}

// resyncInstanceAddon re-provisions the per-project DB on a shared
// instance addon and rotates the password.
export async function resyncInstanceAddon(
  project: string,
  addon: string
): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/resync-instance`,
    { method: "POST" }
  );
}

// repairAddonPassword fixes the helm-chart password drift bug —
// ALTER USER inside the running pod to match the conn secret. Use
// when the SQL console returns "password authentication failed".
export async function repairAddonPassword(
  project: string,
  addon: string
): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/repair-password`,
    { method: "POST" }
  );
}

export async function deleteAddon(project: string, addon: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}`,
    { method: "DELETE" }
  );
}

// updateAddon applies a partial update to spec.{version,size,ha,
// storageSize,database,backup}. Pass undefined for fields you don't
// want to change.
export interface UpdateAddonBody {
  version?: string;
  size?: "small" | "medium" | "large";
  ha?: boolean;
  storageSize?: string;
  database?: string;
  // backup.schedule = "" disables scheduled backups (the chart drops
  // the CronJob). retentionDays = 0 keeps backups forever; the chart
  // skips the prune step in that case.
  backup?: {
    schedule?: string;
    retentionDays?: number;
  };
}

export async function updateAddon(
  project: string,
  addon: string,
  body: UpdateAddonBody
): Promise<KusoAddon> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}`,
    { method: "PATCH", body }
  );
}

// setAddonPlacement pins the addon's StatefulSet to a subset of nodes.
// Pass an empty body to clear (schedule anywhere). Server validates
// that at least one cluster node matches the labels — a 400 with
// "no cluster node matches placement" comes back when the selector
// is unsatisfiable.
export async function setAddonPlacement(
  project: string,
  addon: string,
  body: { labels?: Record<string, string>; nodes?: string[] }
): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/placement`,
    { method: "PUT", body }
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
  key: string,
  // Optional destination addon. Empty/undefined = restore in-place
  // (overwrites the source — destructive). Set to a sibling addon's
  // short name to restore non-destructively into that addon.
  into?: string
): Promise<{ job: string }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/backups/restore`,
    { method: "POST", body: { key, into } }
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
