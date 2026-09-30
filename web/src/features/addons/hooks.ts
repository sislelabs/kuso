"use client";

import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { getSubscribedAddons, setSubscribedAddons, type SubscribedAddons } from "./api";

export const subscribedAddonsQueryKey = (project: string, service: string) =>
  ["projects", project, "services", service, "subscribed-addons"] as const;

export function useSubscribedAddonsForServices(project: string, services: string[]) {
  return useQueries({
    queries: services.map((service) => ({
      queryKey: subscribedAddonsQueryKey(project, service),
      queryFn: () => getSubscribedAddons(project, service),
      enabled: !!project,
    })),
  });
}

// useToggleAddonSubscription adds/removes one addon from one service's
// subscription list, computed from the service's current list.
export function useToggleAddonSubscription(project: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (vars: { service: string; addon: string; on: boolean; current: SubscribedAddons }) => {
      const next = vars.on
        ? Array.from(new Set([...vars.current.subscribed, vars.addon]))
        : vars.current.subscribed.filter((a) => a !== vars.addon);
      await setSubscribedAddons(project, vars.service, next);
    },
    onSettled: (_d, _e, vars) =>
      qc.invalidateQueries({ queryKey: subscribedAddonsQueryKey(project, vars.service) }),
  });
}
