"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toastError } from "@/lib/errors";
import { relativeTime } from "@/lib/format";
import { envGroupName } from "@/lib/env-group";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { usePatchService } from "@/features/services";
import { snapshotRollout, trackRollout } from "@/features/projects/rolloutToast";
import {
  defaultPlan,
  describeRevision,
  markReverting,
  clearReverting,
  previousConfig,
  useReverting,
  useRevertRevision,
  useRevisions,
  type Revision,
} from "@/features/revisions";
import type { KusoEnvironment } from "@/types/projects";

// useRolloutRevert is the one "undo the change that broke the rollout"
// action, shared by the failing-rollout card and the header (and so the
// phone's action bar). Replays the config the previous version ran with,
// or unsets a field that had no earlier value.
export function useRolloutRevert(project: string, service: string, env: KusoEnvironment | undefined) {
  const failing = env?.status?.state === "rollout_failing";
  const revs = useRevisions(project, "service", failing ? service : undefined);
  const revert = useRevertRevision(project);
  const patch = usePatchService(project, service);
  const qc = useQueryClient();
  const reverting = useReverting(project, service, failing);
  const [confirm, setConfirm] = useState(false);
  const list = revs.data ?? [];
  const plan = failing ? previousConfig(list) : null;
  const toDefault = failing && !plan ? defaultPlan(list) : null;
  const change = plan?.change ?? toDefault?.change;
  const changeLabel = change ? describeChange(change) : "";
  const pending = revert.isPending || patch.isPending;

  const onConfirm = async () => {
    const snap = await snapshotRollout(project, service);
    markReverting(project, service);
    const done = {
      onSuccess: () => {
        setConfirm(false);
        trackRollout(snap, {
          label: service,
          qc,
          rollingText: `Reverting ${service}…`,
          doneText: `Reverted ${service}`,
        });
      },
      onError: (e: unknown) => {
        clearReverting(project, service);
        setConfirm(false);
        toastError(e, "Revert failed");
      },
    };
    if (plan) revert.mutate(plan.target.id, done);
    else if (toDefault) patch.mutate(toDefault.patch, done);
  };

  const dialog = (
    <ConfirmDialog
      open={confirm}
      title={plan ? "Revert to the previous config?" : "Revert to the default?"}
      destructive={false}
      confirmLabel="Revert"
      body={
        <span>
          Undoes <strong>{changeLabel}</strong>{" "}
          {plan ? <>by re-applying the config from {relativeTime(plan.target.createdAt)}.</> : <>by going back to {toDefault?.label}.</>}{" "}
          It&apos;s a service setting, so every environment of {service} rolls again
          {env ? <>, starting with {envGroupName(env)}</> : null}.
        </span>
      }
      pending={pending}
      onConfirm={() => void onConfirm()}
      onCancel={() => setConfirm(false)}
    />
  );

  return {
    // A revert is on offer: the rollout is failing and kuso knows what to go back to.
    available: failing && (!!plan || !!toDefault),
    loaded: revs.isSuccess,
    toDefault: !plan && !!toDefault,
    change,
    changeLabel,
    reverting,
    pending,
    request: () => setConfirm(true),
    dialog,
  };
}

function describeChange(rev: Revision): string {
  const v = describeRevision(rev);
  const s = rev.snapshot as { patch?: Record<string, unknown> } | null;
  if (s?.patch && "healthcheck" in s.patch) return "the health check change";
  if (s?.patch && "image" in s.patch) return "the image change";
  return v.label.toLowerCase();
}
