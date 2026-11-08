import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api-client";
import { errorsQueryKey, getProjectErrorSummary, listErrors, projectErrorSummaryQueryKey } from "@/features/services";
import type { ProjectErrorSummary } from "@/features/services/api";

// The product's one definition of "errors": occurrences of a service's
// runtime error groups (build and release-hook output excluded server
// side) over ERRORS_WINDOW. The canvas badge, project card chip, overlay
// tab badge and Errors tab all read it through the project summary, so
// they agree; the logs panel's level chips count something else (error
// lines in view) and say so.
export const ERRORS_WINDOW = "24h";

// errorCountLabel renders a count the same way on every surface:
// "3 errors · 24h".
export function errorCountLabel(n: number, window: string = ERRORS_WINDOW): string {
  return `${n.toLocaleString()} ${n === 1 ? "error" : "errors"} · ${window}`;
}

export interface ServiceErrorCount {
  occurrences: number;
  groups: number;
}

// summaryUnsupported: servers before the summary endpoint 404 (or 405) it.
export function summaryUnsupported(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 404 || e.status === 405);
}

function summaryQueryKey(project: string, since: string, env: string) {
  // env "" shares the cache entry the canvas and project cards use.
  return env ? ([...projectErrorSummaryQueryKey(project, since), env] as const) : projectErrorSummaryQueryKey(project, since);
}

function fetchSummary(project: string, since: string, env: string): Promise<ProjectErrorSummary> {
  if (!env) return getProjectErrorSummary(project, since);
  return api(
    `/api/projects/${encodeURIComponent(project)}/errors/summary?since=${encodeURIComponent(since)}&env=${encodeURIComponent(env)}`,
  );
}

// useServiceErrorCount is a service's error total for the window, from
// the project-wide summary (one request per project, shared). On servers
// without the summary it sums the service's groups instead.
export function useServiceErrorCount(
  project: string,
  service: string,
  since: string = ERRORS_WINDOW,
  env = "",
): { data: ServiceErrorCount | undefined; isPending: boolean; isError: boolean } {
  const summary = useQuery({
    queryKey: summaryQueryKey(project, since, env),
    queryFn: () => fetchSummary(project, since, env),
    enabled: !!project && !!service,
    staleTime: 30_000,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    retry: false,
  });
  const fallback = summaryUnsupported(summary.error);
  const list = useQuery({
    queryKey: errorsQueryKey(project, service, since, env),
    queryFn: () => listErrors(project, service, since, env || undefined),
    enabled: fallback && !!service,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  });
  if (fallback) {
    const groups = list.data;
    return {
      data: groups ? { occurrences: groups.reduce((n, g) => n + g.count, 0), groups: groups.length } : undefined,
      isPending: list.isPending,
      isError: list.isError,
    };
  }
  const s = summary.data?.services[service];
  return {
    data: summary.data ? { occurrences: s?.occurrences ?? 0, groups: s?.groups ?? 0 } : undefined,
    isPending: summary.isPending,
    isError: summary.isError,
  };
}
