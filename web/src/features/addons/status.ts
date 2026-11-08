"use client";

import { useAddonPods } from "@/features/pods";
import { ADDON_STATUS, addonStatusFromPods, type StatusMeta } from "@/lib/status";
import type { KusoAddon } from "@/types/projects";

export interface AddonHealth {
  status: StatusMeta;
  // "2/2 pods ready · 3 restarts", or the reason it isn't live.
  detail?: string;
  pending: boolean;
  // Image tag of the running datastore pod ("16-alpine"), for addons on
  // the chart default. Undefined on servers that don't report it.
  runningVersion?: string;
  runningImage?: string;
}

// runningImageOf reads the additive runningImage/runningVersion fields of
// the addon pods response (newer servers only).
export function runningImageOf(data: unknown): { runningVersion?: string; runningImage?: string } {
  if (!data || typeof data !== "object") return {};
  const d = data as Record<string, unknown>;
  return {
    ...(typeof d.runningVersion === "string" && d.runningVersion ? { runningVersion: d.runningVersion } : {}),
    ...(typeof d.runningImage === "string" && d.runningImage ? { runningImage: d.runningImage } : {}),
  };
}

// useAddonHealth is the overlay's answer to "is this addon up": pod
// readiness folded through the shared vocabulary, with the helm release
// conditions breaking ties (a wedged upgrade fails even with old pods up).
export function useAddonHealth(project: string, addon: string, cr: KusoAddon | undefined): AddonHealth {
  const external = !!cr?.spec.external && !cr?.spec.pooler?.enabled;
  const pods = useAddonPods(project, external ? "" : addon);
  if (external) return { status: ADDON_STATUS.external, detail: "Managed outside kuso", pending: false };
  if (pods.isPending) return { status: ADDON_STATUS.unknown, detail: "Checking pods…", pending: true };
  if (pods.isError) return { status: ADDON_STATUS.unknown, detail: "Couldn't read pods", pending: false };

  const conditions = (cr?.status?.conditions ?? []) as Array<{ type?: string; status?: string; message?: string }>;
  const failure = conditions.find(
    (c) => (c.type === "ReleaseFailed" || c.type === "Irreconcilable") && c.status === "True",
  );
  const deployed = conditions.some((c) => c.type === "Deployed" && c.status === "True");
  const list = pods.data?.pods ?? [];
  const ready = list.filter((p) => p.ready).length;
  const restarts = list.reduce((n, p) => n + (p.restarts ?? 0), 0);
  const kind = addonStatusFromPods({ failed: !!failure, deployed, total: list.length, ready });
  const firstProblem = list.find((p) => !p.ready && p.message)?.message;
  const detail =
    kind === "failing" && failure?.message
      ? failure.message
      : list.length === 0
        ? deployed
          ? "No pods running"
          : "Waiting for the first pod"
        : `${ready}/${list.length} pods ready${restarts ? ` · ${restarts} restart${restarts === 1 ? "" : "s"}` : ""}${
            firstProblem && ready < list.length ? ` · ${firstProblem}` : ""
          }`;
  return { status: ADDON_STATUS[kind], detail, pending: false, ...runningImageOf(pods.data) };
}
