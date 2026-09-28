// Project registry credentials: private-registry logins that runtime=image
// services reference via spec.image.pullSecret. Passwords are write-only —
// the server never returns them, so nothing here models one on read.
import { api } from "@/lib/api-client";

export interface RegistryCredential {
  registry: string;
  username: string;
  secretName: string;
  updatedAt?: string;
}

export interface RegistryLoginBody {
  registry: string;
  username: string;
  password: string;
}

export const registryCredentialsQueryKey = (project: string) =>
  ["projects", project, "registry-credentials"] as const;

const base = (project: string) =>
  `/api/projects/${encodeURIComponent(project)}/registry-credentials`;

export async function listRegistryCredentials(project: string): Promise<RegistryCredential[]> {
  const res = await api<{ credentials: RegistryCredential[] | null }>(base(project));
  return res?.credentials ?? [];
}

export async function loginRegistry(
  project: string,
  body: RegistryLoginBody,
): Promise<RegistryCredential> {
  return api<RegistryCredential>(base(project), { method: "POST", body });
}

export async function logoutRegistry(project: string, registry: string): Promise<void> {
  await api(`${base(project)}/${encodeURIComponent(registry)}`, { method: "DELETE" });
}
