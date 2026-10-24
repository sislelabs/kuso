"use client";

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getProjectMetrics, searchProjectLogs, type ProjectLogSearchResponse } from "./api";

export function useProjectMetrics(project: string) {
  return useQuery({
    queryKey: ["project-metrics", project],
    queryFn: () => getProjectMetrics(project),
    enabled: !!project,
    refetchInterval: 30_000,
  });
}

export const PROJECT_LOG_PAGE_SIZE = 200;

// sinceISO is computed by the caller when the search is committed, so the
// key stays stable between renders and scroll-back pages share one window.
export function useProjectLogSearch(
  project: string,
  params: { q: string; env: string; since: string; sinceISO: string },
  enabled: boolean,
) {
  return useInfiniteQuery<ProjectLogSearchResponse>({
    queryKey: ["project-log-search", project, params.q, params.env, params.since, params.sinceISO],
    queryFn: ({ pageParam }) =>
      searchProjectLogs(project, {
        q: params.q || undefined,
        env: params.env || undefined,
        since: params.sinceISO,
        limit: PROJECT_LOG_PAGE_SIZE,
        beforeId: pageParam as number | undefined,
      }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.nextBeforeId,
    enabled: enabled && !!project,
    staleTime: 5_000,
  });
}
