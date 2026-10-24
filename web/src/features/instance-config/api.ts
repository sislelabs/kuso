import { api } from "@/lib/api-client";
import type { PodSize } from "@/features/cluster-config/api";

// /api/config/* reads + the admin-only preset/runpack mutations
// (server-go handlers/config.go). PodSize and Runpack are untagged Go
// structs, so keys are capitalised and sql.NullString fields arrive as
// {String, Valid}.

export interface NullString {
  String: string;
  Valid: boolean;
}

// What GET /api/config/podsizes actually returns: cluster-config's
// PodSize plus the Description the server always includes.
export interface PodSizeRow extends PodSize {
  Description?: NullString;
}

export interface PodSizeInput {
  Name: string;
  CPURequest: string;
  CPULimit: string;
  MemoryRequest: string;
  MemoryLimit: string;
  Description: string;
}

export const podSizesQueryKey = ["admin", "podsizes"] as const;
export const defaultPodSizeQueryKey = ["admin", "default-podsize"] as const;
export const runpacksQueryKey = ["admin", "runpacks"] as const;
export const instanceInfoQueryKey = ["config", "instance-info"] as const;

// Kubernetes resource.Quantity, loosely: what the API server will accept
// for cpu/memory. Empty means "unset".
const QUANTITY_RE = /^([+-]?[0-9.]+)([eE][+-]?[0-9]+|m|k|M|G|T|P|E|Ki|Mi|Gi|Ti|Pi|Ei)?$/;

export function isValidQuantity(v: string): boolean {
  return v === "" || QUANTITY_RE.test(v);
}

// validatePodSize returns the first problem with the form, or null. The
// server stores whatever it's sent, and a bad quantity only surfaces when
// a service using the preset fails to render. otherNames are the names of
// every other preset: lookups go by name and the first match wins.
export function validatePodSize(p: PodSizeInput, otherNames: string[] = []): string | null {
  const name = p.Name.trim();
  if (!name) return "Name is required.";
  if (name.toLowerCase() === "none") return '"none" is reserved (it means no default).';
  if (otherNames.includes(name)) return `A preset named "${name}" already exists.`;
  const fields: [keyof PodSizeInput, string][] = [
    ["CPURequest", "CPU request"],
    ["CPULimit", "CPU limit"],
    ["MemoryRequest", "Memory request"],
    ["MemoryLimit", "Memory limit"],
  ];
  for (const [k, label] of fields) {
    if (!isValidQuantity(p[k].trim())) return `${label} "${p[k]}" isn't a Kubernetes quantity (e.g. 250m, 1, 512Mi, 2Gi).`;
  }
  return null;
}

function toWire(p: PodSizeInput): Omit<PodSizeRow, "ID"> {
  const d = p.Description.trim();
  return {
    Name: p.Name.trim(),
    CPURequest: p.CPURequest.trim(),
    CPULimit: p.CPULimit.trim(),
    MemoryRequest: p.MemoryRequest.trim(),
    MemoryLimit: p.MemoryLimit.trim(),
    Description: { String: d, Valid: d !== "" },
  };
}

export async function listPodSizeRows(): Promise<PodSizeRow[]> {
  return (await api<PodSizeRow[] | null>("/api/config/podsizes")) ?? [];
}

export async function createPodSize(p: PodSizeInput): Promise<PodSizeRow> {
  return api<PodSizeRow>("/api/config/podsizes", { method: "POST", body: toWire(p) });
}

// PUT replaces every column, so the whole form goes over the wire.
export async function updatePodSize(id: string, p: PodSizeInput): Promise<PodSizeRow> {
  return api<PodSizeRow>(`/api/config/podsizes/${encodeURIComponent(id)}`, {
    method: "PUT",
    body: toWire(p),
  });
}

export async function deletePodSize(id: string): Promise<void> {
  await api<void>(`/api/config/podsizes/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function podSizeToInput(p: PodSizeRow): PodSizeInput {
  return {
    Name: p.Name,
    CPURequest: p.CPURequest,
    CPULimit: p.CPULimit,
    MemoryRequest: p.MemoryRequest,
    MemoryLimit: p.MemoryLimit,
    Description: p.Description?.Valid ? p.Description.String : "",
  };
}

export interface RunpackPhase {
  ID: string;
  Repository: string;
  Tag: string;
  Command?: NullString;
  ReadOnlyAppStorage: boolean;
}

export interface Runpack {
  ID: string;
  Name: string;
  Language: string;
  Fetch?: RunpackPhase | null;
  Build?: RunpackPhase | null;
  Run?: RunpackPhase | null;
}

export async function listRunpacks(): Promise<Runpack[]> {
  return (await api<Runpack[] | null>("/api/config/runpacks")) ?? [];
}

export async function deleteRunpack(id: string): Promise<void> {
  await api<void>(`/api/config/runpacks/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export interface Banner {
  show: boolean;
  text: string;
  bgcolor: string;
  fontcolor: string;
}

// Non-admins only get {enabled, host}; admins get the raw map, which can
// carry credentials, so the UI never renders anything past these two.
export interface RegistryInfo {
  enabled: boolean;
  host?: string;
}

export interface TemplatesInfo {
  enabled: boolean;
  catalogs: unknown[];
}

export interface InstanceInfo {
  banner: Banner;
  registry: RegistryInfo;
  clusterIssuer: string;
  templates: TemplatesInfo;
}

export async function getInstanceInfo(): Promise<InstanceInfo> {
  const [banner, registry, issuer, templates] = await Promise.all([
    api<Partial<Banner>>("/api/config/banner"),
    api<Record<string, unknown>>("/api/config/registry"),
    api<{ clusterissuer: string }>("/api/config/clusterissuer"),
    api<{ enabled?: boolean; catalogs?: unknown }>("/api/config/templates"),
  ]);
  return {
    banner: {
      show: banner.show === true,
      text: typeof banner.text === "string" ? banner.text : "",
      bgcolor: typeof banner.bgcolor === "string" ? banner.bgcolor : "",
      fontcolor: typeof banner.fontcolor === "string" ? banner.fontcolor : "",
    },
    registry: {
      enabled: registry.enabled === true,
      host: typeof registry.host === "string" ? registry.host : undefined,
    },
    clusterIssuer: issuer.clusterissuer,
    templates: {
      enabled: templates.enabled === true,
      catalogs: Array.isArray(templates.catalogs) ? templates.catalogs : [],
    },
  };
}

// catalogLabel picks a display name for one templates catalog entry; the
// shape is free-form on the Kuso CR.
export function catalogLabel(c: unknown): string {
  if (typeof c === "string") return c;
  if (c && typeof c === "object") {
    const o = c as Record<string, unknown>;
    const name = typeof o.name === "string" ? o.name : "";
    const where = typeof o.index === "string" ? o.index : typeof o.url === "string" ? o.url : "";
    if (name || where) return [name, where].filter(Boolean).join(" · ");
  }
  return JSON.stringify(c);
}
