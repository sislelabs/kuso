"use client";

import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { listBackups } from "@/features/projects";
import { addonBackupsQueryKey, isBackupsNotConfigured, runBackupNow } from "./backups";
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

// useAddonBackups is the one backups-list query: the Overview tile, the
// header's Back up now and the Backups tab all read this cache.
export function useAddonBackups(project: string, addon: string, enabled = true) {
  return useQuery({
    queryKey: addonBackupsQueryKey(project, addon),
    queryFn: () => listBackups(project, addon),
    enabled: enabled && !!project && !!addon,
    refetchInterval: (q) => (isBackupsNotConfigured(q.state.error) ? false : 30_000),
    // A 503 "S3 not set up" is an answer, not a blip worth retrying.
    retry: (n, e) => !isBackupsNotConfigured(e) && n < 2,
  });
}

// useBackupNow runs the addon's scheduled backup job once, on demand.
export function useBackupNow(project: string, addon: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => runBackupNow(project, addon),
    onSuccess: (res) => {
      toast.success("Backup started", {
        description: `Job ${res.job}. It shows up under Backups once the upload finishes.`,
      });
      void qc.invalidateQueries({ queryKey: addonBackupsQueryKey(project, addon) });
    },
    onError: (e) => toastError(e, "Couldn't start the backup"),
  });
}
