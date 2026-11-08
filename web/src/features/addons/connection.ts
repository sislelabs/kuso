import { api } from "@/lib/api-client";

// AddonConnection is the masked view of an addon's connection secret.
// `masked` lists the keys whose real value is withheld; their entry in
// `values` is either "" or a redacted preview (a DSN with the password
// replaced). Revealing or copying one goes through revealAddonSecret,
// which the server audits per key.
export interface AddonConnection {
  values: Record<string, string>;
  masked: string[];
  // false when the server predates masked mode and sent everything in
  // the clear (and audited the read): reveal is then local only.
  serverMasked: boolean;
}

const base = (project: string, addon: string) =>
  `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/secret`;

export async function getAddonConnection(project: string, addon: string): Promise<AddonConnection> {
  const res = await api<{ values?: Record<string, string>; masked?: string[] }>(`${base(project, addon)}?masked=1`);
  const values = res.values ?? {};
  if (Array.isArray(res.masked)) {
    return { values, masked: res.masked, serverMasked: true };
  }
  // Older server: plaintext for every key. Mask the same keys client-side
  // so nothing secret is on screen until the user asks.
  return { values, masked: Object.keys(values).filter((k) => isSensitiveConnectionKey(k, values[k] ?? "")), serverMasked: false };
}

// revealAddonSecret fetches one plaintext value. Audited server-side as
// addon.secret_read for that key.
export async function revealAddonSecret(project: string, addon: string, key: string): Promise<string> {
  const res = await api<{ values?: Record<string, string> }>(`${base(project, addon)}?key=${encodeURIComponent(key)}`);
  const v = res.values?.[key];
  if (typeof v !== "string") throw new Error(`${key} isn't in the connection secret`);
  return v;
}

// Mirrors the server's classification: only plainly non-secret keys are
// shown in the clear, and never a URL that carries a password.
export function isSensitiveConnectionKey(key: string, value: string): boolean {
  const plain = /(^|_)(HOST|HOSTNAME|PORT|DATABASE|DB|USER|USERNAME|REGION|BUCKET|SSLMODE)$/i.test(key);
  return !plain || /:\/\/[^/\s:@]*:[^/\s@]*@/.test(value);
}
