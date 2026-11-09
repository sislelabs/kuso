"use client";

import { useAddonPods, type AddonPod } from "@/features/pods";
import { ADDON_STATUS, addonStatusFromPods, addonStatusFromServer, type StatusMeta } from "@/lib/status";
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

// useAddonHealth is the overlay's answer to "is this addon up". The
// server's rollup (addonStatusFromServer: helm conditions + pod readiness
// + stuck) decides the status so the header, overview, canvas and Health
// page agree; the pod list only adds counts, restarts and the running
// version. Older servers without a rollup fold the pod list here.
export function useAddonHealth(project: string, addon: string, cr: KusoAddon | undefined): AddonHealth {
  const external = !!cr?.spec.external && !cr?.spec.pooler?.enabled;
  // A database on the shared instance postgres has no pods of its own.
  const shared = !!cr?.spec.useInstanceAddon;
  const pods = useAddonPods(project, external || shared ? "" : addon);
  return addonHealthFrom(cr, {
    pending: !external && pods.isPending,
    error: pods.isError,
    pods: pods.data?.pods ?? undefined,
    raw: pods.data,
  });
}

export function addonHealthFrom(
  cr: KusoAddon | undefined,
  q: { pending: boolean; error: boolean; pods: readonly AddonPod[] | undefined; raw?: unknown },
): AddonHealth {
  const external = !!cr?.spec.external && !cr?.spec.pooler?.enabled;
  if (external) return { status: ADDON_STATUS.external, detail: "Managed outside kuso", pending: false };
  // The server skips its rollup for these (no pods to read), so the pod
  // fallback below would call every one of them failing. Its Helm
  // conditions are the answer, the same one the canvas node gives.
  if (cr?.spec.useInstanceAddon) {
    const server = addonStatusFromServer(cr);
    return {
      status: ADDON_STATUS[server.status],
      detail: server.detail ?? `Runs on the shared instance database ${cr.spec.useInstanceAddon}`,
      pending: false,
    };
  }
  const list = q.pods ?? [];
  const ready = list.filter((p) => p.ready).length;
  const restarts = list.reduce((n, p) => n + (p.restarts ?? 0), 0);
  const restartsNote = restarts ? ` · ${restarts} restart${restarts === 1 ? "" : "s"}` : "";
  const image = runningImageOf(q.raw);

  const hasRollup = typeof cr?.status?.state === "string" && cr.status.state !== "";
  if (cr && hasRollup) {
    const server = addonStatusFromServer(cr);
    const counts = server.pods ?? (q.pods ? { ready, total: list.length } : undefined);
    const countNote = counts && counts.total > 0 ? `${counts.ready}/${counts.total} pods ready${restartsNote}` : undefined;
    // A problem's reason leads; a healthy addon shows its pod counts.
    const detail = server.status === "live" ? (countNote ?? server.detail) : (server.detail ?? countNote);
    return { status: ADDON_STATUS[server.status], ...(detail ? { detail } : {}), pending: false, ...image };
  }

  if (q.pending) return { status: ADDON_STATUS.unknown, detail: "Checking pods…", pending: true };
  if (q.error) return { status: ADDON_STATUS.unknown, detail: "Couldn't read pods", pending: false };

  const conditions = (cr?.status?.conditions ?? []) as Array<{ type?: string; status?: string; message?: string }>;
  const failure = conditions.find(
    (c) => (c.type === "ReleaseFailed" || c.type === "Irreconcilable") && c.status === "True",
  );
  const deployed = conditions.some((c) => c.type === "Deployed" && c.status === "True");
  const kind = addonStatusFromPods({ failed: !!failure, deployed, total: list.length, ready });
  const firstProblem = list.find((p) => !p.ready && (p.summary || p.message));
  const problemText = firstProblem?.summary || firstProblem?.message;
  const detail =
    kind === "failing" && failure?.message
      ? failure.message
      : list.length === 0
        ? deployed
          ? "No pods running"
          : "Waiting for the first pod"
        : `${ready}/${list.length} pods ready${restartsNote}${problemText && ready < list.length ? ` · ${problemText}` : ""}`;
  return { status: ADDON_STATUS[kind], detail, pending: false, ...image };
}
