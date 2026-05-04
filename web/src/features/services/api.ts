import { api } from "@/lib/api-client";
import type { KusoEnvVar, KusoService } from "@/types/projects";

export async function getService(project: string, service: string): Promise<KusoService> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`);
}

export async function getServiceEnv(project: string, service: string): Promise<{ envVars: KusoEnvVar[] }> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/env`);
}

export async function setServiceEnv(
  project: string,
  service: string,
  envVars: KusoEnvVar[]
): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/env`,
    { method: "POST", body: { envVars } }
  );
}

export interface BuildSummary {
  id: string;
  serviceName: string;
  branch?: string;
  commitSha?: string;
  imageTag?: string;
  status: string;
  startedAt?: string;
  finishedAt?: string;
}

export async function listBuilds(project: string, service: string): Promise<BuildSummary[]> {
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds`);
}

export async function triggerBuild(
  project: string,
  service: string,
  body: { branch?: string; ref?: string } = {}
): Promise<BuildSummary> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds`,
    { method: "POST", body }
  );
}

export async function wakeService(project: string, service: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/wake`,
    { method: "POST" }
  );
}

export async function deleteService(project: string, service: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`,
    { method: "DELETE" }
  );
}

// renameService is clone-then-delete on the server side. The new
// service comes up first, the old comes down second; expect a
// brief 503 window on the live URL during the swap.
export async function renameService(
  project: string,
  service: string,
  newName: string
): Promise<KusoService> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/rename`,
    { method: "POST", body: { newName } }
  );
}

export interface PatchServiceBody {
  port?: number;
  runtime?: string;
  domains?: { host: string; tls?: boolean }[];
  scale?: { min?: number; max?: number; targetCPU?: number };
  sleep?: { enabled?: boolean; afterMinutes?: number };
  volumes?: VolumePatch[];
  placement?: PlacementPatch;
  repo?: PatchRepoBody;
  // Per-service preview opt-out. {disabled:true} skips PR previews
  // for this service even when the project toggle is on.
  // {clear:true} drops the override, falling back to project default.
  previews?: { disabled?: boolean; clear?: boolean };
}

export interface PatchRepoBody {
  url: string;
  branch?: string;
  path?: string;
  installationId?: number;
}

export interface PlacementPatch {
  labels?: Record<string, string>;
  nodes?: string[];
  // Server interprets clear=true as "drop the override, fall back
  // to project default". Distinct from sending an empty object,
  // which means "explicitly schedule anywhere".
  clear?: boolean;
}

export interface VolumePatch {
  name: string;
  mountPath: string;
  sizeGi?: number;
  storageClass?: string;
  accessMode?: string;
}

export async function patchService(
  project: string,
  service: string,
  body: PatchServiceBody
): Promise<KusoService> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`,
    { method: "PATCH", body }
  );
}

export async function listAddonSecretKeys(project: string, addon: string): Promise<{ keys: string[] }> {
  return api(`/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/secret-keys`);
}

export async function getServiceLogs(
  project: string,
  service: string,
  env = "production",
  lines = 200
): Promise<{ project: string; service: string; env: string; lines: { pod: string; line: string }[] }> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs?env=${encodeURIComponent(env)}&lines=${lines}`
  );
}

// ---- Crons ----

export interface KusoCron {
  metadata: { name: string; namespace?: string; creationTimestamp?: string };
  spec: {
    project: string;
    service: string;
    schedule: string;
    command: string[];
    suspend?: boolean;
    concurrencyPolicy?: string;
    activeDeadlineSeconds?: number;
  };
  status?: Record<string, unknown>;
}

export interface CreateCronBody {
  name: string;
  schedule: string;
  command: string[];
  suspend?: boolean;
  concurrencyPolicy?: "Allow" | "Forbid" | "Replace";
  activeDeadlineSeconds?: number;
}

export async function listServiceCrons(project: string, service: string): Promise<KusoCron[]> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons`
  );
}

export async function addCron(project: string, service: string, body: CreateCronBody): Promise<KusoCron> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons`,
    { method: "POST", body }
  );
}

export async function deleteCron(project: string, service: string, name: string): Promise<void> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons/${encodeURIComponent(name)}`,
    { method: "DELETE" }
  );
}

export async function syncCron(project: string, service: string, name: string): Promise<KusoCron> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/crons/${encodeURIComponent(name)}/sync`,
    { method: "POST" }
  );
}

export async function rollbackBuild(
  project: string,
  service: string,
  build: string
): Promise<unknown> {
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/builds/${encodeURIComponent(build)}/rollback`,
    { method: "POST" }
  );
}

// ---- Log search ----

export interface LogLine {
  id: number;
  ts: string;
  pod: string;
  project: string;
  service: string;
  env: string;
  line: string;
}

export interface LogSearchResponse {
  project: string;
  service: string;
  q: string;
  lines: LogLine[];
}

export async function searchServiceLogs(
  project: string,
  service: string,
  params: { q?: string; env?: string; since?: string; until?: string; limit?: number }
): Promise<LogSearchResponse> {
  const sp = new URLSearchParams();
  if (params.q) sp.set("q", params.q);
  if (params.env) sp.set("env", params.env);
  if (params.since) sp.set("since", params.since);
  if (params.until) sp.set("until", params.until);
  if (params.limit) sp.set("limit", String(params.limit));
  const query = sp.toString();
  return api(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/logs/search${query ? "?" + query : ""}`
  );
}
