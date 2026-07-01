"use client";

import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  clearProjectPref,
  listProjectPrefs,
  renameFolder,
  setProjectPref,
  type ProjectPref,
} from "./api";

export const projectPrefsQueryKey = ["me", "project-prefs"] as const;

// useProjectPrefs returns the current user's per-project preferences as a
// Map keyed by project name for O(1) lookup from each card. Prefs are
// small (a handful of rows) and rarely change, so a long staleTime keeps
// the grid from refetching on every focus.
export function useProjectPrefs() {
  const query = useQuery({
    queryKey: projectPrefsQueryKey,
    queryFn: listProjectPrefs,
    staleTime: 60_000,
  });
  // Rebuild the lookup Map only when the underlying prefs data actually
  // changes (dataUpdatedAt advances), not on every render — otherwise the
  // fresh Map ref defeats downstream useMemo deps (e.g. the projects-grid
  // cards memo) that depend on this Map.
  const byProject = useMemo(() => {
    const m = new Map<string, ProjectPref>();
    for (const p of query.data ?? []) m.set(p.project, p);
    return m;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query.dataUpdatedAt]);
  return { ...query, byProject };
}

// useSetProjectPref upserts a single project's pref with an optimistic
// cache update so the star toggle / folder move feels instant. Rolls
// back on error and always refetches to reconcile.
export function useSetProjectPref() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      project,
      starred,
      folder,
    }: {
      project: string;
      starred: boolean;
      folder: string;
    }) => setProjectPref(project, { starred, folder }),
    onMutate: async ({ project, starred, folder }) => {
      await qc.cancelQueries({ queryKey: projectPrefsQueryKey });
      const prev = qc.getQueryData<ProjectPref[]>(projectPrefsQueryKey);
      const next = upsert(prev ?? [], project, starred, folder);
      qc.setQueryData(projectPrefsQueryKey, next);
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(projectPrefsQueryKey, ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectPrefsQueryKey });
    },
  });
}

export function useClearProjectPref() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (project: string) => clearProjectPref(project),
    onMutate: async (project) => {
      await qc.cancelQueries({ queryKey: projectPrefsQueryKey });
      const prev = qc.getQueryData<ProjectPref[]>(projectPrefsQueryKey);
      qc.setQueryData(
        projectPrefsQueryKey,
        (prev ?? []).filter((p) => p.project !== project)
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(projectPrefsQueryKey, ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectPrefsQueryKey });
    },
  });
}

export function useRenameFolder() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ from, to }: { from: string; to: string }) =>
      renameFolder(from, to),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: projectPrefsQueryKey });
    },
  });
}

// upsert applies a star/folder change to a prefs list. A change to the
// default state (unstarred + no folder) removes the row entirely,
// mirroring the server's "no row = default" model so the optimistic
// cache matches what the server will return.
function upsert(
  list: ProjectPref[],
  project: string,
  starred: boolean,
  folder: string
): ProjectPref[] {
  const rest = list.filter((p) => p.project !== project);
  if (!starred && folder === "") return rest;
  return [
    ...rest,
    { project, starred, folder: folder || undefined, updatedAt: new Date().toISOString() },
  ];
}
