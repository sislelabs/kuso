import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { listRevisions, revertRevision, type Revision, type RevisionKind } from "./api";

export const revisionsQueryKey = (project: string, kind: RevisionKind, name: string) =>
  ["projects", project, "revisions", kind, name] as const;

export function useRevisions(project: string, kind: RevisionKind, name: string | undefined) {
  return useQuery<Revision[]>({
    queryKey: revisionsQueryKey(project, kind, name ?? ""),
    queryFn: () => listRevisions(project, kind, name ?? ""),
    enabled: !!project && !!name,
    refetchInterval: 30_000,
  });
}

export function useRevertRevision(project: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => revertRevision(project, id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects", project] });
    },
  });
}
