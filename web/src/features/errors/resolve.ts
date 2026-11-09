import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { errorsQueryKey } from "@/features/services";
import type { ErrorGroup } from "@/features/services";

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

// ErrorGroupState: open (never resolved), recurred (resolved, then seen
// again — the server stamps resolvedAt on it) or resolved (no occurrence
// since it was resolved).
export type ErrorGroupState = "open" | "recurred" | "resolved";

export function errorGroupState(g: { lastSeen: string; resolvedAt?: string }): ErrorGroupState {
  if (!g.resolvedAt) return "open";
  return Date.parse(g.lastSeen) > Date.parse(g.resolvedAt) ? "recurred" : "resolved";
}

// errorRowLabel is an error group row's accessible name: the row wraps
// the title, frames and several meta lines, which a screen reader would
// otherwise read out in full.
export function errorRowLabel(title: string, count: number, lastSeen: string, state: ErrorGroupState): string {
  const t = title.length > 120 ? `${title.slice(0, 119)}…` : title;
  const n = `${count.toLocaleString()} ${count === 1 ? "occurrence" : "occurrences"}`;
  const s = state === "open" ? "" : ` · ${state}`;
  return `${t} · ${n} · last seen ${lastSeen}${s}`;
}

function listWithResolved(project: string, service: string, since: string, env: string): Promise<ErrorGroup[]> {
  const q = new URLSearchParams({ since, resolved: "include" });
  if (env) q.set("env", env);
  return api(`/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/errors?${q}`);
}

// useErrorGroupsWithResolved lists a service's error groups including the
// resolved ones (?resolved=include), so the Errors tab can show them under
// a Resolved filter. Servers without the parameter return open groups only.
export function useErrorGroupsWithResolved(project: string, service: string, since: string, env = "") {
  return useQuery({
    queryKey: [...errorsQueryKey(project, service, since, env), "with-resolved"],
    queryFn: () => listWithResolved(project, service, since, env),
    enabled: !!project && !!service,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  });
}
