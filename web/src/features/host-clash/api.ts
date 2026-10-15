import { api } from "@/lib/api-client";

// Every Ingress host in the cluster (admin-only, sorted, 30s server cache).
export async function listIngressDomains(): Promise<string[]> {
  const res = await api<string[] | null>("/api/kubernetes/domains");
  return res ?? [];
}

export function normalizeHost(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, "")
    .replace(/[/:].*$/, "")
    .replace(/\.+$/, "");
}

// clashingHosts returns the candidate hosts an existing Ingress already
// serves. Exact matches only: a wildcard rule ("*.example.com") is listed
// verbatim by the server and isn't treated as owning every subdomain.
export function clashingHosts(candidates: string[], taken: readonly string[]): string[] {
  const set = new Set(taken.map(normalizeHost));
  const out: string[] = [];
  for (const c of candidates) {
    const h = normalizeHost(c);
    if (h && set.has(h) && !out.includes(h)) out.push(h);
  }
  return out;
}
