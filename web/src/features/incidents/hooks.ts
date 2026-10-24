"use client";

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getIncident,
  INCIDENTS_PAGE_SIZE,
  listIncidents,
  resolveIncident,
  sendIncidentFeedback,
  type IncidentFeedbackBody,
} from "./api";

export const incidentsQueryKey = ["incidents"] as const;

// Offset-paged. The server's truncation signal is a response header the
// api() wrapper doesn't surface, so a full page is taken to mean "maybe
// more"; the worst case is one extra empty fetch.
export function useIncidents(state: string, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: [...incidentsQueryKey, "list", { state }],
    queryFn: ({ pageParam }) =>
      listIncidents({ state: state || undefined, offset: pageParam, limit: INCIDENTS_PAGE_SIZE }),
    initialPageParam: 0,
    getNextPageParam: (last, pages) =>
      last.length === INCIDENTS_PAGE_SIZE ? pages.reduce((n, p) => n + p.length, 0) : undefined,
    enabled,
    // Agent runs move state every few minutes; poll only the first page.
    refetchInterval: (q) => ((q.state.data?.pages.length ?? 0) <= 1 ? 30_000 : false),
  });
}

export function useIncident(id: string | null) {
  return useQuery({
    queryKey: [...incidentsQueryKey, "detail", id],
    queryFn: () => getIncident(id ?? ""),
    enabled: !!id,
    refetchInterval: 15_000,
  });
}

export function useIncidentFeedback() {
  const qc = useQueryClient();
  return useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: ({ id, body }: { id: string; body: IncidentFeedbackBody }) =>
      sendIncidentFeedback(id, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: incidentsQueryKey }),
  });
}

export function useResolveIncident() {
  const qc = useQueryClient();
  return useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (id: string) => resolveIncident(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: incidentsQueryKey }),
  });
}
