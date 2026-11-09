import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api-client";
import { errorsQueryKey, getProjectErrorSummary, listErrors, projectErrorSummaryQueryKey } from "@/features/services";
import type { ProjectErrorSummary } from "@/features/services/api";

// errorsEnvScope is the env filter for a view of one env-group:
// production views count the whole project (the cache the project cards
// share, and rows logged before env tagging), other groups only their own.
export function errorsEnvScope(envGroup: string): string {
  return envGroup && envGroup !== "production" ? envGroup : "";
}

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

export const ERRORS_UNAVAILABLE = "Errors unavailable on this server";

// errorsUnavailable: the summary endpoint is missing (old server) and no
// per-service fallback produced a number.
export function errorsUnavailable(summaryMissing: boolean, services: number, failedLists: number): boolean {
  return summaryMissing && (services === 0 || failedLists === services);
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

// Every reader of the summary uses these options, so the cache entry is
// polled one way no matter which surface mounted it first.
function summaryQuery(project: string, since: string, env: string, enabled: boolean) {
  return {
    queryKey: summaryQueryKey(project, since, env),
    queryFn: () => fetchSummary(project, since, env),
    enabled: enabled && !!project,
    staleTime: 30_000,
    refetchInterval: (q: { state: { status: string } }) => (q.state.status === "error" ? false : 60_000),
    refetchIntervalInBackground: false,
    retry: false,
  };
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
  const summary = useQuery(summaryQuery(project, since, env, !!service));
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

// useProjectErrorCounts is useServiceErrorCount for several services at
// once (canvas nodes, mobile cards, project card chip): the same summary
// query, and on servers without it the same per-service fallback queries
// the Errors tab uses. `total` is undefined until there is data.
export function useProjectErrorCounts(
  project: string,
  services: readonly string[],
  since: string = ERRORS_WINDOW,
  env = "",
): {
  counts: Record<string, ServiceErrorCount | undefined>;
  total: number | undefined;
  // The server has no errors summary and there's nothing to fall back on
  // (no services passed, or every per-service read failed): say
  // ERRORS_UNAVAILABLE instead of rendering a clean zero.
  unavailable: boolean;
} {
  const summary = useQuery(summaryQuery(project, since, env, true));
  const fallback = summaryUnsupported(summary.error);
  const lists = useQueries({
    queries: (fallback ? services : []).map((service) => ({
      queryKey: errorsQueryKey(project, service, since, env),
      queryFn: () => listErrors(project, service, since, env || undefined),
      refetchInterval: 30_000,
      refetchIntervalInBackground: false,
    })),
  });
  const listStamp = lists.map((q) => q.dataUpdatedAt).join(",");
  return useMemo(() => {
    const counts: Record<string, ServiceErrorCount | undefined> = {};
    let total: number | undefined;
    const unavailable = errorsUnavailable(fallback, services.length, lists.filter((q) => q.isError).length);
    if (fallback) {
      services.forEach((service, i) => {
        const groups = lists[i]?.data;
        if (!groups) return;
        const occurrences = groups.reduce((n, g) => n + g.count, 0);
        counts[service] = { occurrences, groups: groups.length };
        total = (total ?? 0) + occurrences;
      });
      return { counts, total, unavailable };
    }
    const data = summary.data;
    if (!data) return { counts, total, unavailable };
    for (const service of services) {
      const s = data.services[service];
      counts[service] = { occurrences: s?.occurrences ?? 0, groups: s?.groups ?? 0 };
    }
    total = Object.values(data.services).reduce((n, s) => n + (s.occurrences || 0), 0);
    return { counts, total, unavailable };
    // lists is a fresh array each render; key off the data stamps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fallback, services, listStamp, summary.data]);
}

// useFleetErrorSummaries reads the per-project summary for many projects
// at once (the home board). Same cache entries and polling as the canvas
// and the project page, so opening a project costs no extra request.
export function useFleetErrorSummaries(projects: readonly string[]): {
  byProject: ReadonlyMap<string, ProjectErrorSummary>;
  unavailable: boolean;
} {
  const results = useQueries({ queries: projects.map((p) => summaryQuery(p, ERRORS_WINDOW, "", true)) });
  const stamp = results.map((q) => q.dataUpdatedAt).join(",");
  return useMemo(() => {
    const byProject = new Map<string, ProjectErrorSummary>();
    results.forEach((q, i) => {
      if (q.data) byProject.set(projects[i], q.data);
    });
    const unavailable = results.length > 0 && results.every((q) => summaryUnsupported(q.error));
    return { byProject, unavailable };
    // results is a new array each render; stamp tracks the data that matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stamp, projects]);
}
