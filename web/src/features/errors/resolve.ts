import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";

function resolvePath(project: string, service: string, fingerprint: string): string {
  return `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/errors/${encodeURIComponent(fingerprint)}/resolve`;
}

// useResolveError resolves (or, with reopen, un-resolves) one error group.
// A resolved group drops out of the list and the counts until it recurs.
export function useResolveError(project: string, service: string) {
  const qc = useQueryClient();
  return useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: ({ fingerprint, reopen }: { fingerprint: string; reopen?: boolean }) =>
      api(resolvePath(project, service, fingerprint), { method: reopen ? "DELETE" : "POST" }),
    onSuccess: () => {
      // Error lists and the project summary both live under the project key.
      qc.invalidateQueries({ queryKey: ["projects", project] });
    },
  });
}

const MAX_WINDOW_MIN = 30 * 24 * 60;

// sinceDeployWindow turns "since the current deploy" into the errors API's
// lookback ("95m"), rounded up to whole minutes and capped at 30 days, so
// the query key only changes when the deploy (or the minute) does.
export function sinceDeployWindow(deployedAt: string | undefined, now: number): string | null {
  const t = deployedAt ? Date.parse(deployedAt) : NaN;
  if (!Number.isFinite(t)) return null;
  const minutes = Math.min(MAX_WINDOW_MIN, Math.max(1, Math.ceil((now - t) / 60_000)));
  return `${minutes}m`;
}
