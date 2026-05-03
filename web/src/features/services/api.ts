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

export interface PatchServiceBody {
  port?: number;
  runtime?: string;
  domains?: { host: string; tls?: boolean }[];
  scale?: { min?: number; max?: number; targetCPU?: number };
  sleep?: { enabled?: boolean; afterMinutes?: number };
  volumes?: VolumePatch[];
  placement?: PlacementPatch;
  repo?: PatchRepoBody;
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
