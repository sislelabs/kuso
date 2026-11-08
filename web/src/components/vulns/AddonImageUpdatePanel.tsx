"use client";

import { useState } from "react";
import { ArrowUpCircle } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { relativeTime } from "@/lib/format";
import { useUpdateAddonImage, type AddonImageUpdate, type AddonImageUpdateState } from "@/features/vulns";

function hour(h: number): string {
  return `${String(((h % 24) + 24) % 24).padStart(2, "0")}:00`;
}

function fixableNote(st: AddonImageUpdateState): string {
  if (st.fixableLatest < 0) return "";
  if (st.fixableRunning < 0) return ` It has ${st.fixableLatest} fixable critical/high findings.`;
  return ` Fixable critical/high: ${st.fixableRunning} now → ${st.fixableLatest} after.`;
}

function statusLine(st: AddonImageUpdateState, auto: boolean, windowHourUTC: number): string {
  const checked = relativeTime(st.checkedAt);
  switch (st.status) {
    case "current":
      return `Running the newest ${st.image} build${checked ? ` (checked ${checked})` : ""}.`;
    case "scanning":
      return `A newer ${st.image} build is out. It's being scanned before anything rolls.`;
    case "ready":
      return auto
        ? `A newer ${st.image} build passed its scan and rolls in the next window (${hour(windowHourUTC)} UTC).${fixableNote(st)}`
        : `A newer ${st.image} build passed its scan.${fixableNote(st)} Update now to apply it.`;
    case "rejected":
      return `Not rolling the newer ${st.image} build: ${st.message ?? "it scans worse than the running one"}.`;
    case "scan-failed":
      return `Couldn't scan the newer ${st.image} build${st.message ? `: ${st.message}` : ""}. It won't roll until a scan succeeds.`;
    case "updating":
      return st.message === "restarting"
        ? `Restarting onto the newer ${st.image} build…`
        : `Pulling the newer ${st.image} build onto the node before restarting…`;
    case "rolling":
      return `Waiting for the addon to come back up on the newer ${st.image} build…`;
    case "rolled-back":
      return st.message ?? `The newer ${st.image} build never became ready, so the previous one was restored.`;
    case "error":
      return `Couldn't check for a newer ${st.image} build${st.message ? `: ${st.message}` : ""}.`;
  }
}

export function AddonImageUpdatePanel({
  project,
  addon,
  update,
  canEdit,
}: {
  project: string;
  addon: string;
  update: AddonImageUpdate;
  canEdit: boolean;
}) {
  const [confirming, setConfirming] = useState(false);
  const mutate = useUpdateAddonImage(project, addon);
  if (!update.available) return null;
  const st = update.state;
  const busy = st?.status === "updating" || st?.status === "rolling" || mutate.isPending;
  const canUpdate = canEdit && st?.status !== "current" && !busy;

  return (
    <section className="space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h3 className="text-sm font-semibold">Image updates</h3>
          <p className="text-xs text-[var(--text-secondary)]">
            {update.auto
              ? `On. Each night between ${hour(update.windowHourUTC)} and ${hour(update.windowHourUTC + 4)} UTC, kuso moves this addon onto the newest build of its version once that build is scanned and has no more fixable critical/high findings. The version never changes. The new image is pulled first, so the restart itself is short; with the connection pooler on, pooled queries wait through it instead of failing.`
              : "Off. An admin can turn on nightly auto-updates under Settings → Vulnerability scanning."}
          </p>
        </div>
        {canEdit && (
          <Button size="sm" variant="outline" onClick={() => setConfirming(true)} disabled={!canUpdate}>
            <ArrowUpCircle />
            {busy ? "Updating…" : "Update now"}
          </Button>
        )}
      </div>
      <p className="text-xs text-[var(--text-primary)]">
        {st
          ? statusLine(st, update.auto, update.windowHourUTC)
          : update.auto
            ? "Not checked yet. The first check runs within 10 minutes."
            : "Not checked yet."}
      </p>
      <p className="text-[11px] text-[var(--text-tertiary)]">
        Findings in programs the image ships prebuilt (like a Go binary&apos;s stdlib) only go away when upstream
        rebuilds the image. If one doesn&apos;t apply to how you use the addon, ignore it.
      </p>
      <ConfirmDialog
        open={confirming}
        title="Update image now?"
        body={`${addon} restarts onto the newest build of ${st?.image ?? "its image"}. The image is pulled first, then the restart takes a few seconds; direct connections drop during it, pooled ones wait. If the new build doesn't become ready within 30 minutes, kuso restores the previous one.`}
        confirmLabel="Update and restart"
        pending={mutate.isPending}
        onCancel={() => setConfirming(false)}
        onConfirm={() =>
          mutate.mutate(undefined, {
            onSuccess: () => {
              setConfirming(false);
              toast.success("Update started: pulling the new image, then restarting");
            },
            onError: (e) => {
              setConfirming(false);
              toast.error(e instanceof Error ? e.message : "Update failed");
            },
          })
        }
      />
    </section>
  );
}
