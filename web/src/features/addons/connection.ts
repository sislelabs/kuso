import { api } from "@/lib/api-client";

// AddonConnection is the masked view of an addon's connection secret.
// `masked` lists the keys whose real value is withheld; their entry in
// `values` is either "" or a redacted preview (a DSN with the password
// replaced). Revealing or copying one goes through revealAddonSecret,
// which the server audits per key.
export interface AddonConnection {
  values: Record<string, string>;
  masked: string[];
  // Set only when the server predates masked mode and sent every value in
  // the clear (and audited that read): reveals are then served from here
  // and `values` holds the client-side redaction.
  legacyPlain?: Record<string, string>;
}

const base = (project: string, addon: string) =>
  `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/secret`;

export async function getAddonConnection(project: string, addon: string): Promise<AddonConnection> {
  const res = await api<{ values?: Record<string, string>; masked?: string[] }>(`${base(project, addon)}?masked=1`);
  const values = res.values ?? {};
  if (Array.isArray(res.masked)) {
    return { values, masked: res.masked };
  }
  // Older server: plaintext for every key. Mask the same keys client-side
  // so nothing secret is on screen until the user asks.
  const masked = Object.keys(values).filter((k) => (values[k] ?? "") !== "" && isSensitiveConnectionKey(k, values[k] ?? ""));
  const display: Record<string, string> = { ...values };
  for (const k of masked) display[k] = redactConnectionURL(values[k] ?? "");
  return { values: display, masked: masked.sort(), legacyPlain: values };
}

// redactConnectionURL keeps a DSN's structure and hides its password, or
// returns "" when the value isn't such a URL.
export function redactConnectionURL(v: string): string {
  try {
    const u = new URL(v);
    if (!u.password || !u.host) return "";
    u.password = "";
    const user = u.username;
    u.username = "";
    const rest = u.toString().slice(u.protocol.length + 2);
    return `${u.protocol}//${user}:••••••••@${rest}`;
  } catch {
    return "";
  }
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
