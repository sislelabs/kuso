import { api } from "@/lib/api-client";
import { env } from "@/lib/env";
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

// EnvGroupSummary mirrors projects.EnvGroupSummary on the server. An
// "env group" is the project-level environment concept (production /
// staging / client-demo) that spans every service + addon. The group
// is grouped-by-label, not its own CRD — see env_groups.go.
export interface EnvGroupSummary {
  name: string;
  project: string;
  // "production" | "preview" | "custom"
  kind: string;
  services: string[];
  addons: string[];
  addonPolicy?: Record<string, "fresh" | "shared">;
  createdAt?: string;
}

export async function listEnvGroups(project: string): Promise<EnvGroupSummary[]> {
  return api(`/api/projects/${encodeURIComponent(project)}/env-groups`);
}

export async function getEnvGroup(
  project: string,
  name: string,
): Promise<EnvGroupSummary> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/env-groups/${encodeURIComponent(name)}`,
  );
}

export async function createEnvGroup(
  project: string,
  body: { name: string; addonPolicy?: Record<string, "fresh" | "shared"> },
): Promise<EnvGroupSummary> {
  return api(`/api/projects/${encodeURIComponent(project)}/env-groups`, {
    method: "POST",
    body,
  });
}

export async function deleteEnvGroup(project: string, name: string): Promise<void> {
  await api(
    `/api/projects/${encodeURIComponent(project)}/env-groups/${encodeURIComponent(name)}?confirm=${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
}

export async function setEnvGroupServiceBranch(
  project: string,
  envName: string,
  serviceShort: string,
  branch: string,
): Promise<void> {
  await api(
    `/api/projects/${encodeURIComponent(project)}/env-groups/${encodeURIComponent(envName)}/services/${encodeURIComponent(serviceShort)}/branch`,
    { method: "PATCH", body: { branch } },
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
    version?: string;
    ha?: boolean;
    storageSize?: string;
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

// enableAddonPublicTCP allocates a port from the cluster's configured
// pool, flips spec.publicTCP on the addon, and returns the allocated
// port. Admin-only server-side. Idempotent: re-enabling returns the
// existing port unchanged.
export async function enableAddonPublicTCP(
  project: string,
  addon: string,
): Promise<{ port: number }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/public-tcp`,
    { method: "POST" },
  );
}

// disableAddonPublicTCP frees the addon's allocated port back to the
// pool and the operator unrenders the IngressRouteTCP on next
// reconcile. Admin-only, idempotent.
export async function disableAddonPublicTCP(
  project: string,
  addon: string,
): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/public-tcp`,
    { method: "DELETE" },
  );
}

export async function deleteAddon(project: string, addon: string): Promise<void> {
  // ?confirm=<addon> is required server-side to acknowledge data
  // loss. The UI's confirm dialog already has the user type the
  // addon name; we pass that value verbatim. The server compares
  // it against the path param and rejects on mismatch.
  return api(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}?confirm=${encodeURIComponent(addon)}`,
    { method: "DELETE" }
  );
}

// updateAddon applies a partial update to spec.{version,size,ha,
// storageSize,database,backup,pooler}. Pass undefined for fields you
// don't want to change.
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
  // pooler.enabled toggles the opt-in PgBouncer pooler (postgres
  // addons only). Omit to leave the current setting unchanged.
  pooler?: {
    enabled: boolean;
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

// --- Structured data browser/editor ---------------------------------
//
// The grid reads paginated rows for one table and writes single rows by
// primary key. A value crosses the wire as { value, isNull } so NULL is
// distinct from "" / 0 / false; the server binds it as a parameter and
// lets Postgres do the final type coercion.

export interface SQLCellValue {
  value: unknown;
  isNull: boolean;
}

export interface SQLColumn {
  name: string;
  dataType: string;
  udtName: string;
  nullable: boolean;
  default?: string;
  ordinal: number;
  isEnum: boolean;
  enumValues?: string[];
}

export interface SQLColumnsResponse {
  columns: SQLColumn[];
  primaryKey: string[];
  editable: boolean;
}

export async function getSQLColumns(
  project: string,
  addon: string,
  schema: string,
  table: string
): Promise<SQLColumnsResponse> {
  const qs = new URLSearchParams({ schema, table }).toString();
  return api<SQLColumnsResponse>(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/sql/columns?${qs}`
  );
}

export interface SQLRowsResponse {
  columns: string[];
  rows: string[][];
  nulls: boolean[][];
  total: number;
  truncated: boolean;
  elapsed: string;
}

export async function getSQLRows(
  project: string,
  addon: string,
  opts: {
    schema: string;
    table: string;
    limit?: number;
    offset?: number;
    orderBy?: string;
    dir?: "asc" | "desc";
  }
): Promise<SQLRowsResponse> {
  const qs = new URLSearchParams({ schema: opts.schema, table: opts.table });
  if (opts.limit != null) qs.set("limit", String(opts.limit));
  if (opts.offset != null) qs.set("offset", String(opts.offset));
  if (opts.orderBy) qs.set("orderBy", opts.orderBy);
  if (opts.dir) qs.set("dir", opts.dir);
  return api<SQLRowsResponse>(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/sql/rows?${qs.toString()}`
  );
}

function rowsURL(project: string, addon: string): string {
  return `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/sql/rows`;
}

export interface SQLWriteResult {
  columns: string[];
  row: string[];
}

export async function insertSQLRow(
  project: string,
  addon: string,
  schema: string,
  table: string,
  values: Record<string, SQLCellValue>
): Promise<SQLWriteResult> {
  return api<SQLWriteResult>(rowsURL(project, addon), {
    method: "POST",
    body: { schema, table, values },
  });
}

export async function updateSQLRow(
  project: string,
  addon: string,
  schema: string,
  table: string,
  pk: Record<string, SQLCellValue>,
  set: Record<string, SQLCellValue>
): Promise<SQLWriteResult> {
  return api<SQLWriteResult>(rowsURL(project, addon), {
    method: "PATCH",
    body: { schema, table, pk, set },
  });
}

export async function deleteSQLRow(
  project: string,
  addon: string,
  schema: string,
  table: string,
  pk: Record<string, SQLCellValue>
): Promise<{ deleted: number }> {
  return api<{ deleted: number }>(rowsURL(project, addon), {
    method: "DELETE",
    body: { schema, table, pk },
  });
}

// --- Config-as-code -------------------------------------------------
//
// getProjectSpec / applyConfig don't go through the `api()` wrapper:
// that helper JSON-stringifies every non-FormData body and is built
// around JSON request/response. The /spec endpoint returns text/yaml
// and /apply takes a raw application/yaml body. Auth still rides the
// HttpOnly session cookie exactly like api() — credentials:"include",
// no Authorization header (see lib/api-client.ts).

// ConfigPlan mirrors spec.Plan on the server — the diff between a
// kuso.yaml document and the live project. WouldDelete is the advisory
// list of resources present live but absent from the YAML; the apply
// path skips deletes by default, so they surface here instead.
export interface ConfigPlan {
  servicesToCreate: string[];
  servicesToUpdate: string[];
  servicesToDelete: string[];
  addonsToCreate: string[];
  addonsToUpdate: string[];
  addonsToDelete: string[];
  cronsToCreate: string[];
  cronsToUpdate: string[];
  cronsToDelete: string[];
  wouldDelete?: string[];
}

// ConfigStepError mirrors spec.StepError — one failed apply step.
export interface ConfigStepError {
  resource: string;
  op: string;
  message: string;
}

// ConfigApplyResult mirrors spec.ApplyResult — the executed plan plus
// any per-step errors. Returned by a non-dry-run apply.
export interface ConfigApplyResult {
  plan: ConfigPlan;
  errors?: ConfigStepError[];
}

// getProjectSpec fetches the project's current state as a kuso.yaml
// document (text, not JSON).
export async function getProjectSpec(project: string): Promise<string> {
  const res = await fetch(
    `${env.apiBase}/api/projects/${encodeURIComponent(project)}/spec`,
    { credentials: "include" },
  );
  if (!res.ok) throw new Error((await res.text()) || `export failed: ${res.status}`);
  return res.text();
}

// applyConfig POSTs a kuso.yaml body. With dryRun the server returns
// the bare ConfigPlan; without it, a ConfigApplyResult (plan + errors).
export async function applyConfig(
  project: string,
  body: string,
  dryRun: true,
): Promise<ConfigPlan>;
export async function applyConfig(
  project: string,
  body: string,
  dryRun: false,
): Promise<ConfigApplyResult>;
export async function applyConfig(
  project: string,
  body: string,
  dryRun: boolean,
): Promise<ConfigPlan | ConfigApplyResult> {
  const res = await fetch(
    `${env.apiBase}/api/projects/${encodeURIComponent(project)}/apply${dryRun ? "?dryRun=1" : ""}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/yaml" },
      body,
      credentials: "include",
    },
  );
  if (!res.ok) throw new Error((await res.text()) || `apply failed: ${res.status}`);
  return res.json();
}
