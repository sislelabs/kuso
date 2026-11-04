"use client";

import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { getSubscribedAddons, setSubscribedAddons, type SubscribedAddons } from "./api";
import { invalidateProjectDescribe } from "@/features/projects/hooks";
import { putSubscriptionRebased } from "./subscriptionDelta";

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
    // Callers pass a per-call onError; skip the global toast so failures toast once.
    meta: { skipGlobalErrorToast: true },
    mutationFn: async (vars: { service: string; addon: string; on: boolean; current: SubscribedAddons }) => {
      // The PUT replaces the whole list, so toggle against a fresh read
      // rather than this tab's cache (`current`), which may predate another
      // user's change and would silently drop it.
      await putSubscriptionRebased(
        async () => (await getSubscribedAddons(project, vars.service)).subscribed,
        (fresh) => (vars.on ? Array.from(new Set([...fresh, vars.addon])) : fresh.filter((a) => a !== vars.addon)),
        (next, expected) => setSubscribedAddons(project, vars.service, next, expected),
      );
    },
    onSettled: () => {
      // Subscribing rewrites the env's envFromSecrets, which drives the
      // canvas edges, env rows and the service Variables tab (its rows are
      // derived server-side from envFromSecrets). The services prefix also
      // covers the subscribed-addons query.
      qc.invalidateQueries({ queryKey: ["projects", project, "services"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      invalidateProjectDescribe(qc, project);
    },
  });
}
