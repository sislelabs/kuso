import { api } from "@/lib/api-client";
import type {
  AddonVulnsResponse,
  AddVulnIgnoreInput,
  PlatformFindingsResponse,
  PlatformVulnsResponse,
  ProjectVulnsResponse,
  ScanAllResponse,
  ScanQueuedResponse,
  ServiceVulnsResponse,
  VulnIgnore,
  VulnOwner,
  VulnPolicy,
  VulnPolicyResponse,
  VulnScanSettings,
} from "./types";

const enc = encodeURIComponent;

export function vulnOwnerPath(owner: VulnOwner): string {
  const coll = owner.kind === "service" ? "services" : "addons";
  return `/api/projects/${enc(owner.project)}/${coll}/${enc(owner.name)}/vulns`;
}

export function getProjectVulns(project: string): Promise<ProjectVulnsResponse> {
  return api<ProjectVulnsResponse>(`/api/projects/${enc(project)}/vulns`);
}

// Findings are always fetched with ignored included; severity/fixable/
// ignored filtering happens client-side so the filter chips stay instant.
export function getServiceVulns(project: string, service: string, env: string): Promise<ServiceVulnsResponse> {
  const q = new URLSearchParams({ env, includeIgnored: "true" });
  return api<ServiceVulnsResponse>(`${vulnOwnerPath({ kind: "service", project, name: service, env })}?${q}`);
}

export function getAddonVulns(project: string, addon: string): Promise<AddonVulnsResponse> {
  return api<AddonVulnsResponse>(`${vulnOwnerPath({ kind: "addon", project, name: addon })}?includeIgnored=true`);
}

export function rescanVulns(owner: VulnOwner): Promise<ScanQueuedResponse> {
  return api<ScanQueuedResponse>(`${vulnOwnerPath(owner)}/scan`, {
    method: "POST",
    body: owner.kind === "service" ? { env: owner.env } : {},
  });
}

export function setServiceVulnPolicy(project: string, service: string, mode: VulnPolicy): Promise<VulnPolicyResponse> {
  return api<VulnPolicyResponse>(`/api/projects/${enc(project)}/services/${enc(service)}/vulns/policy`, {
    method: "PUT",
    body: { mode },
  });
}

export function listVulnIgnores(owner: VulnOwner): Promise<VulnIgnore[]> {
  return api<VulnIgnore[]>(`${vulnOwnerPath(owner)}/ignores`);
}

export function addVulnIgnore(owner: VulnOwner, input: AddVulnIgnoreInput): Promise<VulnIgnore> {
  return api<VulnIgnore>(`${vulnOwnerPath(owner)}/ignores`, { method: "POST", body: input });
}

export function removeVulnIgnore(owner: VulnOwner, id: number): Promise<void> {
  return api<void>(`${vulnOwnerPath(owner)}/ignores/${id}`, { method: "DELETE" });
}

export function getPlatformVulns(): Promise<PlatformVulnsResponse> {
  return api<PlatformVulnsResponse>("/api/admin/vulns/platform");
}

export function getPlatformFindings(digest: string): Promise<PlatformFindingsResponse> {
  return api<PlatformFindingsResponse>(`/api/admin/vulns/platform/findings?digest=${enc(digest)}`);
}

export function scanAllVulns(): Promise<ScanAllResponse> {
  return api<ScanAllResponse>("/api/admin/vulns/scan-all", { method: "POST" });
}

export function getVulnScanSettings(): Promise<VulnScanSettings> {
  return api<VulnScanSettings>("/api/admin/settings/vulnscan");
}

export function putVulnScanSettings(s: VulnScanSettings): Promise<VulnScanSettings> {
  return api<VulnScanSettings>("/api/admin/settings/vulnscan", { method: "PUT", body: s });
}
