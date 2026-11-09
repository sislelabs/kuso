import { api } from "@/lib/api-client";

// Mirrors projects.PodSummary (server-go/internal/projects/pods_ops.go).
export interface ServicePod {
  name: string;
  ready: boolean;
  phase?: string;
  containers?: string[];
  // Waiting/terminated reason (CrashLoopBackOff, ImagePullBackOff, …),
  // its message, and the summed container restart count.
  reason?: string;
  message?: string;
  restarts?: number;
  // reason/message in plain words, when the server could summarize them.
  summary?: string;
  // false for an older ReplicaSet's pods still draining mid-rollout and
  // for release-hook Job pods. Absent on older servers.
  current?: boolean;
  createdAt?: string;
}

// isCurrentPod: only current pods count toward "N/M ready". A missing
// flag (older server) counts as current.
export function isCurrentPod(p: { current?: boolean }): boolean {
  return p.current !== false;
}

export interface ServicePodList {
  namespace: string;
  pods: ServicePod[] | null;
}

// Mirrors addons.AddonPodSummary (server-go/internal/addons/pods.go).
// message is the most specific "why isn't this serving" the server
// could extract; empty for a healthy pod.
export interface AddonPod {
  name: string;
  ready: boolean;
  phase?: string;
  component?: string;
  restarts?: number;
  message?: string;
  summary?: string;
  containers?: string[];
}

export interface AddonPodList {
  namespace: string;
  pods: AddonPod[] | null;
}

// env should be the env CR name. The server's short-form expansion only
// fires for names without a dash, so "preview-pr-7" would be taken as a
// full CR name and 404.
export async function listServicePods(
  project: string,
  service: string,
  env: string,
): Promise<ServicePodList> {
  const qs = env ? `?env=${encodeURIComponent(env)}` : "";
  return api<ServicePodList>(
    `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/pods${qs}`,
  );
}

export async function listAddonPods(project: string, addon: string): Promise<AddonPodList> {
  return api<AddonPodList>(
    `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/pods`,
  );
}
