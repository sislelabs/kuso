"use client";

import { useQuery } from "@tanstack/react-query";
import { listNotificationEventTypes } from "./api";

export const notificationEventTypesQueryKey = ["notifications", "event-types"] as const;

export function useNotificationEventTypes() {
  return useQuery({
    queryKey: notificationEventTypesQueryKey,
    queryFn: listNotificationEventTypes,
    // The catalogue only changes with a server release.
    staleTime: 5 * 60_000,
  });
}
