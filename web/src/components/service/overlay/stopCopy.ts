import type { KusoEnvironment } from "@/types/projects";

// hasPublicIngress: the env answers HTTP from outside the cluster.
// Workers and internal-only services have no ingress, so there is
// nothing to return a 503.
export function hasPublicIngress(env: KusoEnvironment | undefined, internal: boolean, runtime?: string): boolean {
  if (internal || runtime === "worker") return false;
  if (!env) return false;
  return !!env.spec.host || (env.spec.additionalHosts?.length ?? 0) > 0;
}

// stopConsequence is the one sentence for what Stop does, shared by every
// Stop confirm so a worker is never promised a 503.
export function stopConsequence(publicIngress: boolean): string {
  return publicIngress
    ? "Scales the service to zero. Visitors get a 503 until you start it again."
    : "Scales the service to zero. Its background jobs stop until you start it again.";
}
