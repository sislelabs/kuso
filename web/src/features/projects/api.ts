import { api } from "@/lib/api-client";
import type {
  KusoAddon,
  KusoEnvironment,
  KusoProject,
  KusoService,
} from "@/types/projects";

export async function listProjects(): Promise<KusoProject[]> {
  return api<KusoProject[]>("/api/projects");
}

export async function getProject(name: string): Promise<{
  project: KusoProject;
  services: KusoService[];
  environments: KusoEnvironment[];
}> {
  return api(`/api/projects/${encodeURIComponent(name)}`);
}

export async function listServices(project: string): Promise<KusoService[]> {
  return api<KusoService[]>(`/api/projects/${encodeURIComponent(project)}/services`);
}

export async function listEnvironments(
  project: string
): Promise<KusoEnvironment[]> {
  return api<KusoEnvironment[]>(`/api/projects/${encodeURIComponent(project)}/envs`);
}

export async function listAddons(project: string): Promise<KusoAddon[]> {
  return api<KusoAddon[]>(`/api/projects/${encodeURIComponent(project)}/addons`);
}
