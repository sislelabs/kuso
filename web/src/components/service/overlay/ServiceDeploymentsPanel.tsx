"use client";

import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LogStream } from "@/components/logs/LogStream";
import { useBuilds, useTriggerBuild, rollbackBuild } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import type { BuildSummary } from "@/features/services/api";
import type { KusoEnvironment } from "@/types/projects";
import { relativeTime } from "@/lib/format";
import { ChevronDown, ChevronRight, RotateCcw, ExternalLink, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
  env?: KusoEnvironment;
}

// Status drives the badge. ACTIVE is now reserved for the build whose
// image is the one the env is currently running — older successful
// builds become SUPERSEDED so the user can tell which one's live at
// a glance. Without this, every successful build wore an ACTIVE pill
// forever, which lied during a redeploy.
type Status = "active" | "superseded" | "failed" | "running" | "pending" | "unknown";

// formatDuration turns a millisecond span into the kind of label the
// build CI/CDs of the world print: "12s", "1m 04s", "3m 17s",
// "1h 02m". Sub-second spans floor to "0s" rather than disappear so
// a freshly-clicked redeploy shows a live counter immediately.
function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (min < 60) {
    return remSec === 0 ? `${min}m` : `${min}m ${String(remSec).padStart(2, "0")}s`;
  }
  const hr = Math.floor(min / 60);
  const remMin = min % 60;
  return remMin === 0 ? `${hr}h` : `${hr}h ${String(remMin).padStart(2, "0")}m`;
}

// buildDuration returns the time-on-task for a build:
//   - running:   now - startedAt (live counter)
//   - finished:  finishedAt - startedAt
//   - missing:   "" so the renderer skips the whole pill
// Lives in the shared util area so the same shape is used for the
// live + completed cases — flips between them with no layout shift.
function buildDuration(b: BuildSummary, status: Status): string {
  const startMs = b.startedAt ? Date.parse(b.startedAt) : NaN;
  if (!Number.isFinite(startMs)) return "";
  if (status === "running") {
    return formatDuration(Date.now() - startMs);
  }
  const endMs = b.finishedAt ? Date.parse(b.finishedAt) : NaN;
  if (!Number.isFinite(endMs)) return "";
  return formatDuration(endMs - startMs);
}

// useNowTick re-renders every second while `running` is true so the
// live duration display ticks. Returns nothing — the side effect is
// the bumped state. Stops the interval when nothing is running so a
// quiet panel doesn't burn cycles forcing renders.
function useNowTick(running: boolean) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [running]);
}

function classify(b: BuildSummary, activeImageTag?: string): Status {
  const s = (b.status ?? "").toLowerCase();
  if (s === "succeeded") {
    // No env tag yet (fresh service, never deployed) → first
    // successful build is the one that promoted, so it's active.
    if (!activeImageTag) return "active";
    return b.imageTag && b.imageTag === activeImageTag ? "active" : "superseded";
  }
  if (s === "failed") return "failed";
  if (s === "running") return "running";
  if (s === "pending") return "pending";
  return "unknown";
}

function statusBadge(s: Status) {
  const map: Record<Status, { label: string; cls: string }> = {
    active:     { label: "ACTIVE",     cls: "bg-emerald-500/10 text-emerald-400 border-emerald-500/30" },
    superseded: { label: "SUPERSEDED", cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
    failed:     { label: "FAILED",     cls: "bg-red-500/10 text-red-400 border-red-500/30" },
    running:    { label: "BUILDING",   cls: "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30" },
    pending:    { label: "PENDING",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)]" },
    unknown:    { label: "UNKNOWN",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
  };
  const m = map[s];
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-widest border",
        m.cls
      )}
    >
      {m.label}
    </span>
  );
}

export function ServiceDeploymentsPanel({ project, service, env }: Props) {
  const builds = useBuilds(project, service);
  const trigger = useTriggerBuild(project, service);
  const [expanded, setExpanded] = useState<string | null>(null);
  const canDeploy = useCan(Perms.ServicesWrite);
  // Re-render every second while at least one build is running so
  // the in-flight duration display ticks visibly. The hook itself
  // skips the interval when nothing is running so finished-only
  // panels don't burn cycles.
  const anyRunning = (builds.data ?? []).some(
    (b) => (b.status ?? "").toLowerCase() === "running",
  );
  useNowTick(anyRunning);

  const onRedeploy = async () => {
    try {
      await trigger.mutateAsync({});
      toast.success("Build triggered");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to trigger build");
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3 text-xs text-[var(--text-secondary)]">
          {env?.status?.url ? (
            <a
              href={env.status.url as string}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 font-mono text-[var(--accent)] hover:underline"
            >
              {(env.status.url as string).replace(/^https?:\/\//, "")}
              <ExternalLink className="h-3 w-3" />
            </a>
          ) : (
            <span className="font-mono text-[var(--text-tertiary)]">no URL yet</span>
          )}
          {env?.spec.kind && (
            <span className="font-mono text-[var(--text-tertiary)]">{env.spec.kind}</span>
          )}
        </div>
        {canDeploy ? (
          <Button size="sm" onClick={onRedeploy} disabled={trigger.isPending}>
            <RotateCcw className="h-3.5 w-3.5" />
            {trigger.isPending ? "Triggering…" : "Redeploy"}
          </Button>
        ) : (
          <span
            className="font-mono text-[10px] text-[var(--text-tertiary)]"
            title="services:write permission required"
          >
            read-only
          </span>
        )}
      </div>

      {builds.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : (() => {
        // Filter to builds matching the active env's branch. Without
        // this filter the deployments tab listed every build for the
        // service across every env, so a PR-branch build would appear
        // under production. Bug fix: each env shows only its own
        // history.
        const envBranch = env?.spec?.branch;
        const visible = envBranch
          ? (builds.data ?? []).filter((b) => (b.branch ?? "") === envBranch)
          : (builds.data ?? []);
        if (visible.length === 0) {
          return (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
              No builds for this environment yet. Trigger one with the button above or push to the connected branch.
            </p>
          );
        }
        return (
        <ul className="space-y-2">
          {(() => {
            // env.spec.image.tag is the source of truth for "what's
            // actually running." Find the active build by matching
            // imageTag; null when the env hasn't been promoted yet.
            const envImage = (env?.spec as { image?: { tag?: string } } | undefined)?.image;
            const activeTag = envImage?.tag;
            return visible.map((b) => {
              const s = classify(b, activeTag);
              const sha = (b.commitSha ?? "").slice(0, 12);
              const branch = b.branch ?? "—";
              const ts = b.startedAt ?? b.finishedAt;
              const created = ts ? relativeTime(ts) : "—";
              const duration = buildDuration(b, s);
              const isOpen = expanded === b.id;
              return (
                <li
                  key={b.id}
                  className={cn(
                    // overflow-hidden is the fix for the redeploy
                    // layout break — the expanded BuildLogs container
                    // contains a <pre> that grows wider than its
                    // parent and was punching through the rounded card.
                    "overflow-hidden rounded-md border bg-[var(--bg-secondary)]",
                    s === "failed" && "border-red-500/30",
                    s === "active" && "border-emerald-500/30",
                    s === "running" && "border-amber-500/30",
                    !["failed", "active", "running"].includes(s) && "border-[var(--border-subtle)]"
                  )}
                >
                  <div className="flex items-center gap-1 px-3 py-2.5">
                    <button
                      type="button"
                      onClick={() => setExpanded(isOpen ? null : b.id)}
                      className="flex flex-1 items-center gap-3 text-left"
                    >
                      {statusBadge(s)}
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">
                          <span className="font-mono">{sha || "—"}</span>
                          <span className="ml-2 text-xs text-[var(--text-tertiary)]">on {branch}</span>
                        </div>
                        <div className="font-mono text-[10px] text-[var(--text-tertiary)]">
                          {created}
                          {duration && (
                            <>
                              {" · "}
                              <span className={cn(s === "running" && "text-[var(--building)]")}>
                                {duration}
                              </span>
                            </>
                          )}
                        </div>
                      </div>
                    </button>
                    {/* Rollback only for succeeded-but-superseded builds.
                        Server validates phase=succeeded too; the
                        client-side gate is just to hide the noise. */}
                    {s === "superseded" && canDeploy && (
                      <RollbackButton project={project} service={service} buildId={b.id} sha={sha} />
                    )}
                    <button
                      type="button"
                      onClick={() => setExpanded(isOpen ? null : b.id)}
                      className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                    >
                      {isOpen ? (
                        <ChevronDown className="h-4 w-4 shrink-0" />
                      ) : (
                        <ChevronRight className="h-4 w-4 shrink-0" />
                      )}
                    </button>
                  </div>
                  {isOpen && (
                    <div className="min-w-0 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]">
                      <BuildLogs project={project} service={service} buildId={b.id} />
                    </div>
                  )}
                </li>
              );
            });
          })()}
        </ul>
        );
      })()}
    </div>
  );
}

// BuildLogs streams the build pod's logs. LogStream is keyed on env
// today; we encode the build id as env=build:<id> so the server can
// route to the kaniko pod by name. If the server doesn't recognise
// it we fall through to "no logs available" (the server side handles
// that case gracefully).
function BuildLogs({ project, service, buildId }: { project: string; service: string; buildId: string }) {
  return (
    <div className="h-72 p-2">
      <LogStream
        project={project}
        service={service}
        env={`build:${buildId}`}
        height="100%"
      />
    </div>
  );
}

// RollbackButton — tiny inline confirm/yes/no flow that POSTs the
// build's rollback endpoint. Server validates phase=succeeded so the
// only client-side check is "we're on a superseded build" gate.
function RollbackButton({
  project,
  service,
  buildId,
  sha,
}: {
  project: string;
  service: string;
  buildId: string;
  sha: string;
}) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const m = useMutation({
    mutationFn: () => rollbackBuild(project, service, buildId),
    onSuccess: () => {
      toast.success(`Rolled back to ${sha || buildId}`);
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "builds"] });
      // The env list is keyed under "envs" everywhere else (see
      // features/projects/hooks.ts useEnvs); this used to invalidate
      // "environments" which never matched and left the Deployments
      // tab showing stale ACTIVE badges after rollback.
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      setConfirming(false);
    },
    onError: (e) => {
      toast.error(e instanceof Error ? e.message : "Rollback failed");
      setConfirming(false);
    },
  });
  if (!confirming) {
    return (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setConfirming(true);
        }}
        title={`Roll production back to ${sha || buildId}`}
        className="inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] hover:border-amber-500/40 hover:bg-amber-500/5 hover:text-amber-400"
      >
        <Undo2 className="h-3 w-3" />
        rollback
      </button>
    );
  }
  return (
    <div
      onClick={(e) => e.stopPropagation()}
      className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/5 px-1.5 py-1"
    >
      <span className="font-mono text-[10px] text-amber-400">rollback to {sha || buildId.slice(0, 8)}?</span>
      <Button
        size="sm"
        variant="ghost"
        disabled={m.isPending}
        onClick={() => m.mutate()}
        className="h-5 px-2 text-[10px] text-amber-400"
      >
        {m.isPending ? "…" : "yes"}
      </Button>
      <Button
        size="sm"
        variant="ghost"
        onClick={() => setConfirming(false)}
        disabled={m.isPending}
        className="h-5 px-2 text-[10px]"
      >
        no
      </Button>
    </div>
  );
}
