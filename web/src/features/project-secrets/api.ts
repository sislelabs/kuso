// Project shared-secrets API. Each entry becomes one env var in the
// "<project>-shared" Secret that's auto-mounted into every service in
// the project. Used for cross-service integrations (RESEND_API_KEY,
// STRIPE_SECRET_KEY, OPENAI_API_KEY) where every service needs the
// same value.
import { api } from "@/lib/api-client";

export interface SharedSecretsList {
  keys: string[];
}

export const sharedSecretsQueryKey = (project: string) =>
  ["projects", project, "shared-secrets"] as const;

export async function listSharedSecrets(project: string): Promise<SharedSecretsList> {
  return api<SharedSecretsList>(
    `/api/projects/${encodeURIComponent(project)}/shared-secrets`
  );
}

export async function setSharedSecret(
  project: string,
  // force overrides the 409 `shadowed` guard (a service-level env var
  // with the same key would otherwise win over the shared value).
  body: { key: string; value: string; force?: boolean }
): Promise<void> {
  await api(`/api/projects/${encodeURIComponent(project)}/shared-secrets`, {
    method: "PUT",
    body,
  });
}

export async function unsetSharedSecret(project: string, key: string): Promise<void> {
  await api(
    `/api/projects/${encodeURIComponent(project)}/shared-secrets/${encodeURIComponent(key)}`,
    { method: "DELETE" }
  );
}
