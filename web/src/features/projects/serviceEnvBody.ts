import type { CreateServiceEnvBody } from "./api";

export interface ServiceEnvForm {
  name: string;
  branch: string;
  host: string;
  shareAddons: boolean;
  // "" = start the env's database empty.
  seedFrom: string;
  // Kinds picked for per-env copies; [] = all stateful kinds.
  addons: string[];
}

// Mirrors the server's envNameRE + reserved names.
const ENV_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export function buildServiceEnvBody(f: ServiceEnvForm): CreateServiceEnvBody | { error: string } {
  const name = f.name.trim();
  if (!name) return { error: "Name required" };
  if (name === "production" || name.startsWith("pr-")) {
    return { error: 'Names "production" and "pr-*" are reserved for kuso-managed environments' };
  }
  if (!ENV_NAME_RE.test(name) || name.length > 32) return { error: "Name: lowercase, dashes, ≤32 chars" };
  const branch = f.branch.trim();
  if (!branch) return { error: "Branch required — which branch should this environment build from?" };
  const body: CreateServiceEnvBody = { name, branch };
  if (f.host.trim()) body.host = f.host.trim().toLowerCase();
  if (f.shareAddons) {
    body.shareAddons = true;
  } else {
    if (f.seedFrom.trim()) body.seedFrom = f.seedFrom.trim();
    if (f.addons.length > 0) body.addons = [...f.addons];
  }
  return body;
}
