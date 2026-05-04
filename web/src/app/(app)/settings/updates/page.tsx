"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { CheckCircle2, AlertTriangle, RefreshCw, Clock, Package } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

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

  const version = useQuery({
    queryKey: ["system", "version"],
    queryFn: () => api<VersionState>("/api/system/version"),
    refetchInterval: 30_000,
  });

  const status = useQuery({
    queryKey: ["system", "update-status"],
    queryFn: () => api<UpdateStatus>("/api/system/update/status"),
    refetchInterval: 5_000,
  });

  const start = useMutation({
    mutationFn: () => api<{ job: string }>("/api/system/update", { method: "POST" }),
    onSuccess: (res) => {
      toast.success(`Update started: ${res.job}`);
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

  if (version.isPending) {
    return (
      <div className="mx-auto max-w-2xl p-6 lg:p-8">
        <Skeleton className="h-32 w-full rounded-md" />
      </div>
    );
  }
  const v = version.data!;
  const inFlight = !!status.data?.phase && status.data.phase !== "" && status.data.phase !== "done" && status.data.phase !== "failed";

  return (
    <div className="mx-auto max-w-2xl space-y-4 p-6 lg:p-8">
      <header className="flex items-start gap-3">
        <Package className="h-5 w-5 shrink-0 text-[var(--text-tertiary)]" />
        <div className="min-w-0 flex-1">
          <h1 className="font-heading text-xl font-semibold tracking-tight">Updates</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Self-update the kuso server + operator. Polls{" "}
            <span className="font-mono">github.com/sislelabs/kuso/releases</span> every 6h.
          </p>
        </div>
        {/* Manual poll trigger: bypasses the 6h ticker so an admin who
            just shipped a new release can pull it forward immediately
            instead of waiting (or restarting the pod). */}
        <Button
          variant="neutral"
          size="sm"
          onClick={() => refresh.mutate()}
          disabled={refresh.isPending}
        >
          <RefreshCw className={cn("h-3 w-3", refresh.isPending && "animate-spin")} />
          {refresh.isPending ? "Checking…" : "Check for updates"}
        </Button>
      </header>

      {/* Status card */}
      <section
        className={cn(
          "rounded-md border p-4",
          v.needsUpdate
            ? v.canAutoUpgrade
              ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)]"
              : "border-amber-500/30 bg-amber-500/5"
            : "border-emerald-500/30 bg-emerald-500/5"
        )}
      >
        <div className="flex items-start gap-3">
          {v.needsUpdate ? (
            <AlertTriangle className={cn("h-5 w-5 shrink-0", v.canAutoUpgrade ? "text-[var(--accent)]" : "text-amber-400")} />
          ) : (
            <CheckCircle2 className="h-5 w-5 shrink-0 text-emerald-400" />
          )}
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-semibold">
              {v.needsUpdate
                ? v.canAutoUpgrade
                  ? `Update available: ${v.latest}`
                  : `Update available: ${v.latest} — manual upgrade required`
                : "Up to date"}
            </h2>
            <div className="mt-1 flex flex-wrap items-center gap-3 font-mono text-[10px] text-[var(--text-tertiary)]">
              <span>current {v.current || "—"}</span>
              {v.latest && <span>latest {v.latest}</span>}
              {v.lastChecked && (
                <span title={v.lastChecked}>
                  <Clock className="mr-1 inline h-2.5 w-2.5" />
                  checked {relTime(v.lastChecked)}
                </span>
              )}
            </div>
            {v.blockedReason && (
              <p className="mt-2 text-[11px] text-amber-400">{v.blockedReason}</p>
            )}
            {v.lastCheckError && (
              <p className="mt-2 text-[11px] text-red-400">poll error: {v.lastCheckError}</p>
            )}
          </div>
          {v.needsUpdate && v.canAutoUpgrade && canUpdate && (
            <Button size="sm" onClick={() => start.mutate()} disabled={start.isPending || inFlight}>
              <RefreshCw className={cn("h-3 w-3", (start.isPending || inFlight) && "animate-spin")} />
              {inFlight ? "Updating…" : start.isPending ? "Starting…" : "Update"}
            </Button>
          )}
        </div>

        {/* Components table when we have a manifest. */}
        {v.manifest?.components && (
          <div className="mt-4 grid grid-cols-2 gap-2 border-t border-[var(--border-subtle)] pt-3 text-[11px]">
            <div>
              <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                server
              </div>
              <div className="mt-0.5 truncate font-mono">
                {v.manifest.components.server?.image ?? "—"}
              </div>
            </div>
            <div>
              <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                operator
              </div>
              <div className="mt-0.5 truncate font-mono">
                {v.manifest.components.operator?.image ?? "—"}
              </div>
            </div>
          </div>
        )}
      </section>

      {/* In-flight update phase indicator */}
      {inFlight && status.data && (
        <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
          <h3 className="text-sm font-semibold">Update in progress</h3>
          <div className="mt-2 font-mono text-[11px]">
            <div>
              <span className="text-[var(--text-tertiary)]">phase</span>{" "}
              <span className="text-[var(--accent)]">{status.data.phase}</span>
            </div>
            {status.data.message && (
              <div className="mt-0.5 truncate text-[var(--text-secondary)]">{status.data.message}</div>
            )}
            {status.data.started && (
              <div className="mt-0.5 text-[var(--text-tertiary)]">
                started {relTime(status.data.started)}
              </div>
            )}
          </div>
          <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
            The kuso server pod will be replaced mid-update. Your session may briefly drop —
            refresh once it reconnects.
          </p>
        </section>
      )}

      {status.data?.phase === "done" && !v.needsUpdate && (
        <section className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3 text-[11px]">
          Last upgrade completed{" "}
          {status.data.updated ? <span className="font-mono">{relTime(status.data.updated)}</span> : ""}.
        </section>
      )}
      {status.data?.phase === "failed" && (
        <section className="rounded-md border border-red-500/30 bg-red-500/5 p-3 text-[11px] text-red-400">
          Last upgrade failed: {status.data.message || "(no message)"}
        </section>
      )}

      {/* Release notes */}
      {v.manifest?.notes && (
        <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
          <h3 className="text-sm font-semibold">Release notes ({v.manifest.version})</h3>
          <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-[var(--text-secondary)]">
            {v.manifest.notes}
          </pre>
        </section>
      )}

      {!canUpdate && v.needsUpdate && (
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          You don&apos;t have <span className="text-[var(--text-secondary)]">system:update</span>{" "}
          permission. Ask an instance admin to apply this upgrade.
        </p>
      )}
    </div>
  );
}

function relTime(iso: string): string {
  try {
    const t = new Date(iso).getTime();
    const ago = Math.max(0, (Date.now() - t) / 1000);
    if (ago < 60) return "just now";
    if (ago < 3600) return `${Math.floor(ago / 60)}m ago`;
    if (ago < 86400) return `${Math.floor(ago / 3600)}h ago`;
    return `${Math.floor(ago / 86400)}d ago`;
  } catch {
    return iso;
  }
}
