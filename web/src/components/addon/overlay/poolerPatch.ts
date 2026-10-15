import type { UpdateAddonBody } from "@/features/projects";

export interface PoolerForm {
  enabled: boolean;
  // External backend only: the provider's endpoint.
  host: string;
  port: string;
  // "" = chart default (25).
  poolSize: string;
}

export function poolerForm(p?: { enabled?: boolean; host?: string; port?: number; poolSize?: number }): PoolerForm {
  return {
    enabled: !!p?.enabled,
    host: p?.host ?? "",
    port: p?.port ? String(p.port) : "",
    poolSize: p?.poolSize ? String(p.poolSize) : "",
  };
}

// poolerPatch returns the PATCH `pooler` block for the changed fields, or
// undefined when nothing changed. external=true also pins externalBackend
// so the pooler targets host:port instead of an in-cluster pod.
export function poolerPatch(
  next: PoolerForm,
  base: PoolerForm,
  external: boolean,
): { pooler: NonNullable<UpdateAddonBody["pooler"]> } | { error: string } | undefined {
  const out: NonNullable<UpdateAddonBody["pooler"]> = {};
  if (next.enabled !== base.enabled) out.enabled = next.enabled;
  if (next.poolSize.trim() !== base.poolSize.trim()) {
    const raw = next.poolSize.trim();
    const n = raw === "" ? 0 : Number(raw);
    if (!Number.isInteger(n) || n < 0 || n > 10000) return { error: "Pool size must be a whole number (blank = default 25)" };
    out.poolSize = n;
  }
  if (external) {
    if (next.host.trim() !== base.host.trim()) out.host = next.host.trim();
    if (next.port.trim() !== base.port.trim()) {
      const raw = next.port.trim();
      const n = raw === "" ? 0 : Number(raw);
      if (!Number.isInteger(n) || n < 0 || n > 65535) return { error: "Port must be 1–65535 (blank = 5432)" };
      out.port = n;
    }
    if (next.enabled && !next.host.trim()) return { error: "Set the provider host the pooler connects to" };
    if (Object.keys(out).length > 0) out.externalBackend = true;
  }
  return Object.keys(out).length > 0 ? { pooler: out } : undefined;
}
