import { useMutation, useQueryClient } from "@tanstack/react-query";
import { buildsQueryKey } from "@/features/services/hooks";
import { invalidateProjectDescribe } from "@/features/projects/hooks";

// Call sites pass a per-call onError, which the global MutationCache
// toast can't see; without this every failure toasts twice.
const selfHandledErrors = { skipGlobalErrorToast: true } as const;
import { retryRelease, rollbackToBuild } from "./api";

export function useRollbackToBuild(project: string, service: string, env: string) {
  const qc = useQueryClient();
  return useMutation({
    meta: selfHandledErrors,
    mutationFn: ({ build, force }: { build: string; force?: boolean }) =>
      rollbackToBuild(project, service, build, env, force),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      invalidateProjectDescribe(qc, project);
    },
  });
}

export function useRetryRelease(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    meta: selfHandledErrors,
    mutationFn: (build: string) => retryRelease(project, service, build),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
    },
  });
}
