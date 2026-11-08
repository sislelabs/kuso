"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useCan, Perms } from "@/features/auth";
import { CheckCircle2, AlertTriangle, RefreshCw } from "lucide-react";
import { SettingsPage, SettingsPageLoading } from "@/components/settings/SettingsPage";
import { SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { Markdown } from "@/components/settings/Markdown";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { relativeTime } from "@/lib/format";
import { isUpdateInFlight } from "./updatePhase";

interface VersionState {
  current: string;
  latest: string;
  needsUpdate: boolean;
  canAutoUpgrade: boolean;
  blockedReason?: string;
  manifest?: {
    version: string;
    publishedAt?: string;
    notes?: string;
    breaking?: boolean;
    components?: { server?: { image?: string }; operator?: { image?: string } };
  };
  lastChecked?: string;
  lastCheckError?: string;
}

interface UpdateStatus {
  phase: string;
  message?: string;
  started?: string;
  updated?: string;
}

// /settings/updates lets an admin see "is there a newer kuso?" and
// click Update to roll the cluster. The actual work happens in a
// kube Job; this page just surfaces state and pokes start/poll.
export default function UpdatesPage() {
  const qc = useQueryClient();
  const canUpdate = useCan(Perms.SystemUpdate);
  const [confirmOpen, setConfirmOpen] = useState(false);

  const version = useQuery({
    queryKey: ["system", "version"],
    queryFn: () => api<VersionState>("/api/system/version"),
    refetchInterval: 30_000,
  });

  const status = useQuery({
    queryKey: ["system", "update-status"],
    queryFn: () => api<UpdateStatus>("/api/system/update/status"),
    // Fast only while a rollout runs; idle, the status only changes
    // when someone presses Update (which invalidates this query).
    refetchInterval: (q) => {
      return isUpdateInFlight(q.state.data?.phase) ? 5_000 : 60_000;
    },
  });

  const start = useMutation({
    mutationFn: () => api<{ job: string }>("/api/system/update", { method: "POST" }),
    onSuccess: () => {
      setConfirmOpen(false);
      toast.success("Update started");
      qc.invalidateQueries({ queryKey: ["system", "update-status"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Update failed"),
  });

  // Manual GH poll. The 6h background ticker is fine for "is there
  // a new release?" but useless when the user JUST shipped one and
  // wants to roll the cluster now. POSTs to /version/refresh, which
  // runs one synchronous tick + returns the fresh State.
  const refresh = useMutation({
    mutationFn: () => api<VersionState>("/api/system/version/refresh", { method: "POST" }),
    onSuccess: (res) => {
      // Replace the cached version snapshot directly so the UI reflects
      // the new "checked just now" timestamp without an extra round-trip.
      qc.setQueryData(["system", "version"], res);
      if (res.lastCheckError) {
        toast.error(`Poll failed: ${res.lastCheckError}`);
      } else if (res.needsUpdate) {
        toast.success(`Update available: ${res.latest}`);
      } else {
        toast.success(`Up to date (${res.current})`);
      }
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Refresh failed"),
  });

  const checkButton = (
    // Bypasses the 6h ticker so an admin who just shipped a release can
    // pull it forward without waiting or restarting the pod.
    <Button variant="outline" size="sm" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
      <RefreshCw className={cn("h-3 w-3", refresh.isPending && "animate-spin")} />
      {refresh.isPending ? "Checking…" : "Check for updates"}
    </Button>
  );
  const description = "kuso checks for new releases every 6 hours. Nothing installs until you press Update.";

  if (version.isPending) {
    return <SettingsPageLoading title="Updates" rows={1} />;
  }
  if (version.isError) {
    return (
      <SettingsPage title="Updates" description={description}>
        <QueryErrorState what="version status" error={version.error} onRetry={() => void version.refetch()} />
      </SettingsPage>
    );
  }
  const v = version.data;
  const pollFailed = !!v.lastCheckError;
  const inFlight = isUpdateInFlight(status.data?.phase);
  const checked = v.lastChecked && !isZeroTime(v.lastChecked) ? v.lastChecked : null;

  const headline = v.needsUpdate
    ? v.canAutoUpgrade
      ? `Update available: ${v.latest}`
      : `Update available: ${v.latest} (manual upgrade required)`
    : pollFailed
      ? "Couldn't check for updates"
      : "You're up to date";
  const StatusIcon = v.needsUpdate || pollFailed ? AlertTriangle : CheckCircle2;
  const iconColor = pollFailed && !v.needsUpdate
    ? "text-[var(--error)]"
    : v.needsUpdate
      ? v.canAutoUpgrade
        ? "text-[var(--accent)]"
        : "text-[var(--warning)]"
      : "text-[var(--success)]";

  return (
    <SettingsPage title="Updates" description={description} actions={checkButton}>
      <SettingsSection
        footerHint={
          <>
            {checked ? (
              <span title={new Date(checked).toLocaleString()}>Last checked {relativeTime(checked)}</span>
            ) : (
              "Not checked yet"
            )}
            {status.data?.phase === "done" && status.data.updated && (
              <span title={status.data.updated}> · last upgrade finished {relativeTime(status.data.updated)}</span>
            )}
          </>
        }
        footer={
          v.needsUpdate && v.canAutoUpgrade && canUpdate ? (
            <Button size="sm" onClick={() => setConfirmOpen(true)} disabled={start.isPending || inFlight}>
              <RefreshCw className={cn("h-3 w-3", (start.isPending || inFlight) && "animate-spin")} />
              {inFlight ? "Updating…" : start.isPending ? "Starting…" : `Update to ${v.latest}`}
            </Button>
          ) : undefined
        }
      >
        <div className="flex items-start gap-3">
          <StatusIcon className={cn("mt-0.5 h-5 w-5 shrink-0", iconColor)} />
          <div className="min-w-0 flex-1">
            <h2 className="text-base font-semibold text-[var(--text-primary)]">{headline}</h2>
            <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-4">
              <div>
                <dt className="text-xs text-[var(--text-secondary)]">Running</dt>
                <dd className="mt-0.5 font-mono text-[var(--text-primary)]">{v.current || "—"}</dd>
              </div>
              <div>
                <dt className="text-xs text-[var(--text-secondary)]">Latest</dt>
                <dd className="mt-0.5 font-mono text-[var(--text-primary)]">{v.latest || "—"}</dd>
              </div>
              {v.manifest?.publishedAt && (
                <div>
                  <dt className="text-xs text-[var(--text-secondary)]">Released</dt>
                  <dd className="mt-0.5 text-[var(--text-primary)]" title={v.manifest.publishedAt}>
                    {relativeTime(v.manifest.publishedAt)}
                  </dd>
                </div>
              )}
              {v.manifest?.breaking && (
                <div>
                  <dt className="text-xs text-[var(--text-secondary)]">Compatibility</dt>
                  <dd className="mt-0.5 font-medium text-[var(--warning)]">Breaking changes</dd>
                </div>
              )}
            </dl>
            {v.blockedReason && <p className="mt-3 text-sm text-[var(--warning)]">{v.blockedReason}</p>}
            {v.lastCheckError && (
              <p className="mt-3 text-sm text-[var(--error)]">
                Poll error: <span className="font-mono text-xs">{v.lastCheckError}</span>
              </p>
            )}
            {!canUpdate && v.needsUpdate && (
              <p className="mt-3 text-sm text-[var(--text-secondary)]">
                Applying updates needs the <code className="font-mono text-xs">system:update</code> permission. Ask an
                instance admin.
              </p>
            )}
          </div>
        </div>

        {v.manifest?.components && (
          <dl className="grid gap-3 border-t border-[var(--border-subtle)] pt-4 text-sm sm:grid-cols-2">
            <div className="min-w-0">
              <dt className="text-xs text-[var(--text-secondary)]">Server image</dt>
              <dd className="mt-0.5 truncate font-mono text-xs text-[var(--text-primary)]" title={v.manifest.components.server?.image}>
                {v.manifest.components.server?.image ?? "—"}
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-xs text-[var(--text-secondary)]">Operator image</dt>
              <dd className="mt-0.5 truncate font-mono text-xs text-[var(--text-primary)]" title={v.manifest.components.operator?.image}>
                {v.manifest.components.operator?.image ?? "—"}
              </dd>
            </div>
          </dl>
        )}
      </SettingsSection>

      <ConfirmDialog
        open={confirmOpen}
        title={`Update kuso to ${v.latest}?`}
        body={
          <div className="space-y-2">
            <p>
              <span className="font-mono">{v.current || "—"}</span> → <span className="font-mono">{v.latest}</span>
              {v.manifest?.breaking && (
                <span className="ml-2 rounded-sm bg-[var(--warning-subtle)] px-1.5 py-0.5 text-xs font-medium text-[var(--warning)]">
                  Breaking
                </span>
              )}
            </p>
            <p>The UI disconnects briefly while kuso restarts.</p>
          </div>
        }
        confirmLabel="Update"
        destructive={!!v.manifest?.breaking}
        pending={start.isPending}
        onConfirm={() => start.mutate()}
        onCancel={() => setConfirmOpen(false)}
      />

      {inFlight && status.data && (
        <SettingsNotice
          tone="info"
          title={
            <>
              Update in progress · <span className="font-mono text-xs">{status.data.phase}</span>
            </>
          }
        >
          {status.data.message && <p className="truncate">{status.data.message}</p>}
          {status.data.started && <p>Started {relativeTime(status.data.started)}.</p>}
          <p className="mt-1 text-xs">
            The kuso server pod is replaced mid-update, so your session may drop briefly. Refresh once it reconnects.
          </p>
        </SettingsNotice>
      )}

      {status.data?.phase === "failed" && (
        <SettingsNotice tone="error" title="Last upgrade failed">
          {status.data.message || "No message from the updater."}
        </SettingsNotice>
      )}
      {status.data?.phase === "rolled-back" && (
        <SettingsNotice tone="warning" title="Last upgrade was rolled back">
          {status.data.message || "No message from the updater."}
        </SettingsNotice>
      )}
      {status.data?.phase === "rollback-failed" && (
        <SettingsNotice tone="error" title="Last upgrade failed and its rollback failed too">
          {status.data.message || "No message from the updater."}
        </SettingsNotice>
      )}

      {v.manifest?.notes && (
        <SettingsSection title={`Release notes · ${v.manifest.version}`}>
          <Markdown source={v.manifest.notes} />
        </SettingsSection>
      )}
    </SettingsPage>
  );
}

// The server serialises an unset Go time.Time as 0001-01-01; treat it as
// "never checked" rather than rendering a ~2000-year-old timestamp.
function isZeroTime(iso: string): boolean {
  const t = Date.parse(iso);
  return Number.isNaN(t) || new Date(t).getUTCFullYear() < 2000;
}
