import { api } from "@/lib/api-client";

// Mirrors projects.PodSummary (server-go/internal/projects/pods_ops.go).
export interface ServicePod {
  name: string;
  ready: boolean;
  phase?: string;
  containers?: string[];
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
