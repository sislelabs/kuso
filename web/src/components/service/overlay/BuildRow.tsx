"use client";

// Per-build row + inline expand/collapse log viewer + per-row action
// chips (rollback / cancel). Extracted from ServiceDeploymentsPanel
// in the v0.12 refactor so the panel itself can stay close to its
// data-fetching + filtering logic without sprawling into row-level
// presentation. No behaviour change vs the pre-split shape.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { LogStream } from "@/components/logs/LogStream";
import { rollbackBuild, cancelBuild } from "@/features/services";
import type { BuildSummary } from "@/features/services/api";
import { relativeTime } from "@/lib/format";
import { ChevronDown, ChevronRight, Undo2, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { useState } from "react";

export type BuildRowStatus =
  | "active"
  | "superseded"
  | "failed"
  | "running"
  | "pending"
  | "queued"
  | "cancelled"
  | "unknown";

// statusBadge renders the small mono pill on the left of each row.
// Kept here so the row + the badge live in one file (the panel only
// needs the row component).
export function StatusBadge({ s }: { s: BuildRowStatus }) {
  const map: Record<BuildRowStatus, { label: string; cls: string }> = {
    active:     { label: "ACTIVE",     cls: "bg-emerald-500/10 text-emerald-400 border-emerald-500/30" },
    superseded: { label: "SUPERSEDED", cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
    failed:     { label: "FAILED",     cls: "bg-red-500/10 text-red-400 border-red-500/30" },
    running:    { label: "BUILDING",   cls: "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30" },
    pending:    { label: "PENDING",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)]" },
    queued:     { label: "QUEUED",     cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)] border-dashed" },
    cancelled:  { label: "CANCELLED",  cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
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

// triggerLabel renders the "by X" suffix shown on each build row.
//   - source=user + user=alice  → "by alice"
//   - source=user (no name)     → "by you"
//   - source=webhook + user=bob → "by bob (webhook)"
//   - source=webhook (no user)  → "via webhook"
//   - source=api / system       → "via API" / "via system"
//   - none                      → "" (renderer skips the suffix)
function triggerLabel(b: BuildSummary): string {
  const src = b.triggeredBy ?? "";
  const user = b.triggeredByUser ?? "";
  if (src === "user") return user ? `by ${user}` : "by you";
  if (src === "webhook") return user ? `by ${user} (webhook)` : "via webhook";
  if (src === "api") return "via API";
  if (src === "system") return "via system";
  return "";
}

export interface BuildRowProps {
  project: string;
  service: string;
  // env-group short name (production / staging / preview-pr-N) so
  // the rollback POST scopes to the right env CR. Empty defaults
  // to production server-side (pre-v0.17.1 behaviour).
  env: string;
  build: BuildSummary;
  status: BuildRowStatus;
  duration: string;
  isOpen: boolean;
  canDeploy: boolean;
  onToggle: () => void;
}

export function BuildRow({
  project,
  service,
  env,
  build: b,
  status: s,
  duration,
  isOpen,
  canDeploy,
  onToggle,
}: BuildRowProps) {
  const sha = (b.commitSha ?? "").slice(0, 12);
  const branch = b.branch ?? "—";
  const ts = b.startedAt ?? b.finishedAt;
  const created = ts ? relativeTime(ts) : "—";
  return (
    <li
      className={cn(
        // overflow-hidden keeps the expanded log <pre> from punching
        // through the rounded card border on wide builds.
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
          onClick={onToggle}
          className="flex flex-1 items-center gap-3 text-left"
        >
          <StatusBadge s={s} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">
              <span className="font-mono">{sha || "—"}</span>
              <span className="ml-2 text-xs text-[var(--text-tertiary)]">on {branch}</span>
            </div>
            {b.commitMessage && (
              <div className="truncate text-xs text-[var(--text-secondary)]">{b.commitMessage}</div>
            )}
            {b.status === "failed" && b.errorMessage && (
              <div
                className="truncate font-mono text-[11px] text-red-300/90"
                title={b.errorMessage}
              >
                ✗ {b.errorMessage}
              </div>
            )}
            <div className="font-mono text-[10px] text-[var(--text-tertiary)]">
              {created}
              {duration && (
                <>
                  {" · "}
                  <span className={cn(s === "running" && "text-[var(--building)]")}>{duration}</span>
                </>
              )}
              {triggerLabel(b) && (
                <>
                  {" · "}
                  <span>{triggerLabel(b)}</span>
                </>
              )}
            </div>
          </div>
        </button>
        {s === "superseded" && canDeploy && (
          <RollbackButton project={project} service={service} env={env} buildId={b.id} sha={sha} />
        )}
        {(s === "running" || s === "pending" || s === "queued") && canDeploy && (
          <CancelButton project={project} service={service} buildId={b.id} />
        )}
        <button
          type="button"
          onClick={onToggle}
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
          <BuildErrorBanner message={b.status === "failed" ? b.errorMessage : undefined} />
          <BuildLogs project={project} service={service} buildId={b.id} />
        </div>
      )}
    </li>
  );
}

// BuildErrorBanner renders the sticky red banner above the log viewer
// when the build has an extracted failure cause. archiveLogs (server-
// side) scans the tail logs + kubelet's terminated reason and stamps
// the hit into kuso.sislelabs.com/build-message; the API surfaces it
// on BuildSummary.errorMessage. Without this, users were hand-grepping
// 200-600 lines of kaniko log noise to find the one-line cause.
function BuildErrorBanner({ message }: { message?: string }) {
  if (!message) return null;
  return (
    <div className="border-b border-red-500/40 bg-red-500/10 px-3 py-2 text-[12px] text-red-200">
      <div className="flex items-start gap-2">
        <span aria-hidden className="select-none">
          ✗
        </span>
        <div className="min-w-0 flex-1">
          <div className="font-mono text-[10px] uppercase tracking-widest text-red-300/80">
            build failure cause
          </div>
          <div className="mt-0.5 break-words font-mono text-[11px] leading-snug">{message}</div>
        </div>
      </div>
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
      <LogStream project={project} service={service} env={`build:${buildId}`} height="100%" />
    </div>
  );
}

// CancelButton — POSTs the build's cancel endpoint. No confirm step:
// cancelling a build is reversible (the user can just trigger a new
// one) and a confirm dialog on top of a wedged build is friction.
function CancelButton({
  project,
  service,
  buildId,
}: {
  project: string;
  service: string;
  buildId: string;
}) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: () => cancelBuild(project, service, buildId),
    onSuccess: () => {
      toast.success("Build cancelled");
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "builds"] });
    },
    onError: (e) => {
      toast.error(e instanceof Error ? e.message : "Cancel failed");
    },
  });
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        m.mutate();
      }}
      disabled={m.isPending}
      title="Cancel this build"
      className="inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] hover:border-red-500/40 hover:bg-red-500/5 hover:text-red-400 disabled:opacity-50"
    >
      <X className="h-3 w-3" />
      {m.isPending ? "…" : "cancel"}
    </button>
  );
}

// RollbackButton — tiny inline confirm/yes/no flow that POSTs the
// build's rollback endpoint. Server validates phase=succeeded so the
// only client-side check is "we're on a superseded build" gate.
function RollbackButton({
  project,
  service,
  env,
  buildId,
  sha,
}: {
  project: string;
  service: string;
  env: string;
  buildId: string;
  sha: string;
}) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const m = useMutation({
    mutationFn: () => rollbackBuild(project, service, buildId, env),
    onSuccess: () => {
      toast.success(`Rolled back to ${sha || buildId}`);
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "builds"] });
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
      <span className="font-mono text-[10px] text-amber-400">
        rollback to {sha || buildId.slice(0, 8)}?
      </span>
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
