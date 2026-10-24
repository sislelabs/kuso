import type { CreateProjectBody } from "./mutations";

export interface CreateProjectForm {
  name: string;
  description: string;
  baseDomain: string;
  namespace: string;
  repoURL: string;
  repoBranch: string;
  // 0 = none.
  installationId: number;
  previewsEnabled: boolean;
  previewsTTLDays: string;
}

export const DEFAULT_PREVIEW_TTL_DAYS = 7;

// buildCreateProjectBody maps the new-project form onto POST /api/projects.
// Blank optional fields are omitted so the server applies its defaults.
export function buildCreateProjectBody(f: CreateProjectForm): CreateProjectBody | { error: string } {
  const ttlRaw = f.previewsTTLDays.trim();
  const ttl = ttlRaw ? Number(ttlRaw) : DEFAULT_PREVIEW_TTL_DAYS;
  if (!Number.isInteger(ttl) || ttl < 1 || ttl > 30) {
    return { error: "Preview TTL must be a whole number of days from 1 to 30." };
  }
  const ns = f.namespace.trim();
  if (ns && (ns.length > 63 || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(ns))) {
    return { error: "Namespace: lowercase letters, digits and dashes, at most 63 characters." };
  }
  const repoURL = f.repoURL.trim();
  if (!repoURL && f.repoBranch.trim()) return { error: "Set a default repo URL to pin its branch." };
  return {
    name: f.name.trim(),
    description: f.description.trim() || undefined,
    baseDomain: f.baseDomain.trim() || undefined,
    namespace: ns || undefined,
    defaultRepo: repoURL ? { url: repoURL, defaultBranch: f.repoBranch.trim() || undefined } : undefined,
    github: f.installationId > 0 ? { installationId: f.installationId } : undefined,
    previews: { enabled: f.previewsEnabled, ttlDays: ttl },
  };
}
