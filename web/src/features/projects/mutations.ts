"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/lib/api-client";
import { stopProject, startProject } from "./api";
import {
  envsQueryKey,
  projectQueryKey,
  projectsQueryKey,
  servicesQueryKey,
} from "./hooks";

export interface UpdateProjectBody {
  description?: string | null;
  baseDomain?: string | null;
  previews?: { enabled?: boolean; ttlDays?: number };
  defaultRepo?: { url?: string; defaultBranch?: string };
  // alwaysOn=true overrides every per-service sleep config so all
  // services in this project run with scale-to-zero disabled.
  alwaysOn?: boolean;
  // incidentMonitoring=true opts the project into the incident-response
  // agent (it only investigates opted-in projects).
  incidentMonitoring?: boolean;
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

// useSetIncidentMonitoring flips one project's incident-agent opt-in.
// Standalone (not keyed to a single project) so the incident-agent
// settings page can drive a whole list of toggles. Invalidates the
// projects list so every row reflects the new state.
export function useSetIncidentMonitoring() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, enabled }: { name: string; enabled: boolean }) =>
      updateProject(name, { incidentMonitoring: enabled }),
    onSuccess: (_data, { name }) => {
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

// invalidateProjectViews refreshes every surface that renders a
// project's live/stopped state: the projects list, the per-project
// describe-summary the cards read from, and the services/envs the
// canvas + overlay read from. Called by stop/start so the card badge
// and canvas flip without a hard refresh.
function invalidateProjectViews(
  qc: ReturnType<typeof useQueryClient>,
  name: string,
) {
  qc.invalidateQueries({ queryKey: projectsQueryKey });
  qc.invalidateQueries({ queryKey: projectQueryKey(name) });
  // The projects grid cards read services + envs off this composite
  // key, NOT the individual services/envs queries.
  qc.invalidateQueries({ queryKey: ["projects", name, "describe-summary"] });
  qc.invalidateQueries({ queryKey: servicesQueryKey(name) });
  qc.invalidateQueries({ queryKey: envsQueryKey(name) });
}

// useStopProject hard-stops every service in the project. The 202 is
// only an ack — the operator scales pods to 0 asynchronously — so we
// invalidate broadly and let the pollers pick up the final state.
export function useStopProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => stopProject(name),
    onSuccess: (_data, name) => {
      invalidateProjectViews(qc, name);
      toast.success(`Stopping ${name}`);
    },
    onError: (err, name) => {
      toast.error(
        `Failed to stop ${name}: ${err instanceof Error ? err.message : String(err)}`,
      );
    },
  });
}

// useStartProject clears the hard-stop on every service in the project.
export function useStartProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => startProject(name),
    onSuccess: (_data, name) => {
      invalidateProjectViews(qc, name);
      toast.success(`Starting ${name}`);
    },
    onError: (err, name) => {
      toast.error(
        `Failed to start ${name}: ${err instanceof Error ? err.message : String(err)}`,
      );
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
