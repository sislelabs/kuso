"use client";

// Per-build row + inline expand/collapse log viewer + per-row actions
// (rollback / cancel / retry release).

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LogStream } from "@/components/logs/LogStream";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { cancelBuild } from "@/features/services";
import type { BuildSummary, BuildFailureClass } from "@/features/services/api";
import {
  buildNote,
  isBranchMismatch,
  useRetryRelease,
  useRollbackToBuild,
  type BuildRowStatus,
  type DeployBuild,
} from "@/features/builds";
import { relativeTime } from "@/lib/format";
import { ChevronDown, ChevronRight, Undo2, X, Copy, Check, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { useState } from "react";

export type { BuildRowStatus };

const CHIP =
  "inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] disabled:opacity-50";

// statusBadge renders the small mono pill on the left of each row.
// Kept here so the row + the badge live in one file (the panel only
// needs the row component). queuePos is the build's 1-based place in
// the cluster-wide build queue — rendered as "QUEUED #3" so users can
// see how many builds are ahead of theirs; only meaningful (and only
// read) when s=queued.
export function StatusBadge({ s, queuePos }: { s: BuildRowStatus; queuePos?: number }) {
  const map: Record<BuildRowStatus, { label: string; cls: string }> = {
    active:     { label: "ACTIVE",     cls: "bg-emerald-500/10 text-emerald-400 border-emerald-500/30" },
    superseded: { label: "SUPERSEDED", cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
    failed:     { label: "FAILED",     cls: "bg-red-500/10 text-red-400 border-red-500/30" },
    // release-failed: the image BUILT fine but its release hook (e.g. a
    // DB migration) failed, so kuso refused to promote it. Distinct label
    // from FAILED so the operator knows the build is good and the fix is
    // in the release step, not the build.
    "release-failed": { label: "RELEASE FAILED", cls: "bg-orange-500/10 text-orange-400 border-orange-500/30" },
    running:    { label: "BUILDING",   cls: "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30" },
    pending:    { label: "PENDING",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)]" },
    queued:     { label: "QUEUED",     cls: "bg-[var(--bg-tertiary)] text-[var(--text-secondary)] border-[var(--border-subtle)] border-dashed" },
    cancelled:  { label: "CANCELLED",  cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
    unknown:    { label: "UNKNOWN",    cls: "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]" },
  };
  const m = map[s];
  const label = s === "queued" && queuePos ? `QUEUED #${queuePos}` : m.label;
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-widest border",
        m.cls
      )}
    >
      {label}
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
export function triggerLabel(b: BuildSummary): string {
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
  build: DeployBuild;
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
  const note = buildNote(b, s);
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
          <StatusBadge s={s} queuePos={b.queuePosition} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">
              <span className="font-mono">{sha || "—"}</span>
              <span className="ml-2 text-xs text-[var(--text-tertiary)]">on {branch}</span>
            </div>
            {b.commitMessage && (
              <div className="truncate text-xs text-[var(--text-secondary)]">{b.commitMessage}</div>
            )}
            {b.promoteHold && (s === "running" || s === "pending") && (
              <div className="truncate text-xs text-amber-400" title={b.promoteHold}>
                ⏸ promotion held — {b.promoteHold}
              </div>
            )}
            {s === "release-failed" && (
              <div className="truncate text-xs text-orange-400" title={b.errorMessage}>
                Migration failed — previous version still live
              </div>
            )}
            {s === "failed" && b.errorMessage && (
              <div
                className="truncate font-mono text-[11px] text-red-300/90"
                title={b.errorMessage}
              >
                ✗ {b.errorMessage}
              </div>
            )}
            {note && (
              <div className="truncate text-xs text-[var(--text-tertiary)]" title={note}>
                {note}
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
        {/* Rollback needs the build's image. Once it ages past the
            image-retention window the sweep prunes the registry tag and
            blanks b.imageTag; show that instead of silently dropping the chip. */}
        {s === "superseded" && canDeploy && !!b.imageTag && (
          <RollbackButton project={project} service={service} env={env} buildId={b.id} sha={sha} />
        )}
        {s === "superseded" && canDeploy && !b.imageTag && (
          <span
            title="The registry image for this build was deleted by the image-retention sweep, so it can't be rolled back to."
            className="inline-flex cursor-not-allowed items-center gap-1 rounded-md border border-dashed border-[var(--border-subtle)] px-2 py-1 font-mono text-[10px] text-[var(--text-tertiary)]"
          >
            <Undo2 className="h-3 w-3" />
            image pruned
          </span>
        )}
        {s === "release-failed" && canDeploy && (
          <RetryReleaseButton project={project} service={service} buildId={b.id} sha={sha} />
        )}
        {(s === "running" || s === "pending" || s === "queued") && canDeploy && (
          <CancelButton project={project} service={service} buildId={b.id} />
        )}
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={isOpen}
          aria-label={isOpen ? "Hide build log" : "Show build log"}
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
          {s === "release-failed" ? (
            <>
              <ReleaseFailure message={b.errorMessage} job={b.releaseJob} logTail={b.releaseLogTail} />
              {/* Older servers don't send the release log; the build log is all we have. */}
              {!b.releaseLogTail && <BuildLogs project={project} service={service} buildId={b.id} />}
            </>
          ) : (
            <>
              <BuildErrorBanner
                message={s === "failed" ? b.errorMessage : undefined}
                failureClass={s === "failed" ? b.failureClass : undefined}
              />
              <BuildLogs project={project} service={service} buildId={b.id} />
            </>
          )}
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
//
// When the build also carries a structured failureClass with a
// remediation, we render the richer card: the classifier's summary,
// the remediation title + prose, and a copy-pasteable fix block. The
// bare errorMessage path is the fallback for builds the classifier
// couldn't (or hasn't yet) tagged.
function BuildErrorBanner({
  message,
  failureClass,
}: {
  message?: string;
  failureClass?: BuildFailureClass;
}) {
  const rem = failureClass?.remediation;
  if (!message && !failureClass) return null;
  return (
    <div className="border-b border-red-500/40 bg-red-500/10 px-3 py-2 text-[12px] text-red-200">
      <div className="flex items-start gap-2">
        <span aria-hidden className="select-none">
          ✗
        </span>
        <div className="min-w-0 flex-1 space-y-2">
          <div>
            <div className="font-mono text-[10px] uppercase tracking-widest text-red-300/80">
              build failure cause
            </div>
            <div className="mt-0.5 break-words font-mono text-[11px] leading-snug">
              {failureClass?.summary || message}
            </div>
            {failureClass?.lineHint && (
              <div className="mt-0.5 break-words font-mono text-[10px] text-red-300/70">
                {failureClass.lineNum ? `line ${failureClass.lineNum}: ` : ""}
                {failureClass.lineHint}
              </div>
            )}
          </div>

          {rem && (
            <div className="rounded-md border border-red-500/30 bg-[var(--bg-primary)]/40 p-2">
              <div className="text-[11px] font-semibold text-red-100">{rem.title}</div>
              {rem.detail && (
                <p className="mt-0.5 text-[11px] leading-snug text-red-200/90">{rem.detail}</p>
              )}
              {rem.fix && <FixBlock fix={rem.fix} lang={rem.fixLang} />}
              {rem.docsAnchor && (
                <a
                  href={rem.docsAnchor}
                  target="_blank"
                  rel="noreferrer"
                  className="mt-1.5 inline-block font-mono text-[10px] text-red-200/80 underline underline-offset-2 hover:text-red-100"
                >
                  read the docs →
                </a>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// FixBlock is the copy-pasteable remediation snippet. lang labels the
// block (shell / dockerfile / yaml …) and is purely cosmetic — we
// don't ship a syntax highlighter, just a language tag + mono pre.
function FixBlock({ fix, lang }: { fix: string; lang?: string }) {
  const [copied, setCopied] = useState(false);
  const onCopy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(fix);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("clipboard unavailable");
    }
  };
  return (
    <div className="mt-1.5 overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-2 py-1">
        <span className="font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
          {lang || "fix"}
        </span>
        <button
          type="button"
          onClick={onCopy}
          aria-label="Copy fix"
          className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          {copied ? (
            <>
              <Check className="h-3 w-3 text-emerald-500" /> copied
            </>
          ) : (
            <>
              <Copy className="h-3 w-3" /> copy
            </>
          )}
        </button>
      </div>
      <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words p-2 font-mono text-[11px] leading-snug text-[var(--text-secondary)]">
        {fix}
      </pre>
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

// ReleaseFailure replaces the build-failure banner for release-failed
// builds: the image built fine, the release hook (migration) didn't.
function ReleaseFailure({ message, job, logTail }: { message?: string; job?: string; logTail?: string }) {
  return (
    <div className="border-b border-orange-500/40 bg-orange-500/10 px-3 py-2 text-[12px]">
      <div
        className="font-mono text-[10px] uppercase tracking-widest text-orange-300/80"
        title={job ? `job ${job}` : undefined}
      >
        release failed
      </div>
      {message && (
        <div className="mt-0.5 break-words font-mono text-[11px] leading-snug text-orange-200">{message}</div>
      )}
      {logTail && (
        <pre className="mt-1.5 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2 font-mono text-[11px] leading-snug text-[var(--text-secondary)]">
          {logTail}
        </pre>
      )}
    </div>
  );
}

function RetryReleaseButton({
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
  const [open, setOpen] = useState(false);
  const m = useRetryRelease(project, service);
  const run = () =>
    m.mutate(buildId, {
      onSuccess: (res) =>
        toast.success(res.job ? `Release retry started (${res.job})` : "Release retry started"),
      onError: (e) => toast.error(e instanceof Error ? e.message : "Retry failed"),
      onSettled: () => setOpen(false),
    });
  return (
    <>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setOpen(true);
        }}
        disabled={m.isPending}
        title="Re-run the release hook for this build"
        className={cn(CHIP, "hover:border-orange-500/40 hover:bg-orange-500/5 hover:text-orange-400")}
      >
        <RefreshCw className="h-3 w-3" />
        retry release
      </button>
      <ConfirmDialog
        open={open}
        title={`Retry release for ${sha || buildId}?`}
        destructive={false}
        confirmLabel="Retry release"
        body="Re-runs the release hook (migration). The build goes live if it succeeds."
        pending={m.isPending}
        onConfirm={run}
        onCancel={() => setOpen(false)}
      />
    </>
  );
}

// RollbackButton re-points the env at this build. The server refuses a
// build from another branch with a 400; we surface its message and let
// the user force it.
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
  const [step, setStep] = useState<"idle" | "confirm" | "mismatch">("idle");
  const [mismatch, setMismatch] = useState("");
  const m = useRollbackToBuild(project, service, env);
  const target = env || "production";
  const run = (force: boolean) =>
    m.mutate(
      { build: buildId, force },
      {
        onSuccess: () => {
          toast.success(`Rolled back to ${sha || buildId}`);
          setStep("idle");
        },
        onError: (e) => {
          if (!force && isBranchMismatch(e)) {
            setMismatch(e.message);
            setStep("mismatch");
            return;
          }
          toast.error(e instanceof Error ? e.message : "Rollback failed");
          setStep("idle");
        },
      },
    );
  return (
    <>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          setStep("confirm");
        }}
        title={`Roll ${target} back to ${sha || buildId}`}
        className={cn(CHIP, "hover:border-amber-500/40 hover:bg-amber-500/5 hover:text-amber-400")}
      >
        <Undo2 className="h-3 w-3" />
        rollback
      </button>
      <ConfirmDialog
        open={step === "confirm"}
        title={`Roll ${target} back to ${sha || buildId}?`}
        destructive={false}
        confirmLabel="Roll back"
        body={
          <span>
            <strong>{target}</strong> switches to this build&apos;s image. The next push deploys over it.
          </span>
        }
        pending={m.isPending}
        onConfirm={() => run(false)}
        onCancel={() => setStep("idle")}
      />
      <ConfirmDialog
        open={step === "mismatch"}
        title="Different branch"
        confirmLabel="Roll back anyway"
        body={<span className="break-words">{mismatch}</span>}
        pending={m.isPending}
        onConfirm={() => run(true)}
        onCancel={() => setStep("idle")}
      />
    </>
  );
}
