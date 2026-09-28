import { api, ApiError } from "@/lib/api-client";

// Where custom-domain DNS should point. source="none" means the server
// couldn't find a public address (no LoadBalancer IP, no node address).
export interface IngressTargets {
  ips: string[];
  hostnames: string[];
  source: "loadbalancer" | "nodes" | "none";
}

const EMPTY: IngressTargets = { ips: [], hostnames: [], source: "none" };

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : [];
}

export function normalizeIngressTargets(raw: unknown): IngressTargets {
  if (!raw || typeof raw !== "object") return EMPTY;
  const r = raw as Record<string, unknown>;
  const ips = strings(r.ips);
  const hostnames = strings(r.hostnames);
  const source =
    r.source === "loadbalancer" || r.source === "nodes" ? r.source : "none";
  if (ips.length === 0 && hostnames.length === 0) return EMPTY;
  return { ips, hostnames, source };
}

// Older servers don't have the endpoint; treat 404 as "unknown" so the
// UI hides the hint instead of showing an error.
export async function getIngressTargets(): Promise<IngressTargets> {
  try {
    return normalizeIngressTargets(await api<unknown>("/api/config/ingress"));
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return EMPTY;
    throw e;
  }
}
