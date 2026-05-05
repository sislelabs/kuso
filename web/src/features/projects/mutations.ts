"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { projectQueryKey, projectsQueryKey } from "./hooks";

export interface UpdateProjectBody {
  description?: string | null;
  baseDomain?: string | null;
  previews?: { enabled?: boolean; ttlDays?: number };
  defaultRepo?: { url?: string; defaultBranch?: string };
  // alwaysOn=true overrides every per-service sleep config so all
  // services in this project run with scale-to-zero disabled.
  alwaysOn?: boolean;
}

async function updateProject(name: string, body: UpdateProjectBody): Promise<unknown> {
  return api(`/api/projects/${encodeURIComponent(name)}`, {
    method: "PATCH",
    body,
  });
}

async function deleteProject(name: string): Promise<void> {
  return api(`/api/projects/${encodeURIComponent(name)}`, { method: "DELETE" });
}

export interface CreateProjectBody {
  name: string;
  description?: string;
  baseDomain?: string;
  defaultRepo?: { url: string; defaultBranch?: string };
  github?: { installationId?: number };
  previews?: { enabled?: boolean; ttlDays?: number };
}

async function createProject(body: CreateProjectBody): Promise<unknown> {
  return api("/api/projects", { method: "POST", body });
}

export function useUpdateProject(name: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateProjectBody) => updateProject(name, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: projectQueryKey(name) });
      qc.invalidateQueries({ queryKey: projectsQueryKey });
    },
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => deleteProject(name),
    onSuccess: (_data, name) => {
      qc.invalidateQueries({ queryKey: projectsQueryKey });
      qc.removeQueries({ queryKey: projectQueryKey(name) });
    },
  });
}

export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateProjectBody) => createProject(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: projectsQueryKey });
    },
  });
}
