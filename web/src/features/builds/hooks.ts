import { useMutation, useQueryClient } from "@tanstack/react-query";
import { buildsQueryKey } from "@/features/services/hooks";
import { retryRelease, rollbackToBuild } from "./api";

export function useRollbackToBuild(project: string, service: string, env: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ build, force }: { build: string; force?: boolean }) =>
      rollbackToBuild(project, service, build, env, force),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
    },
  });
}

export function useRetryRelease(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (build: string) => retryRelease(project, service, build),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
    },
  });
}
