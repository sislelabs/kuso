"use client";

// One deploy-history row: status, commit, meta, the row's actions, and the
// expandable build log (failure summary first for failed builds).

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Ban,
  CheckCircle2,
  ChevronDown,
  CircleDashed,
  Clock,
  Loader2,
  MoreHorizontal,
  RefreshCw,
  RotateCcw,
  Undo2,
  X,
  XCircle,
} from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { buildTriggerMessage, cancelBuild, useTriggerBuild } from "@/features/services";
import type { BuildFailureClass } from "@/features/services/api";
import {
  buildNote,
  buildRefLabel,
  githubCommitUrl,
  githubCompareUrl,
  isBranchMismatch,
  useRetryRelease,
  useRollbackToBuild,
  type BuildRowStatus,
  type DeployBuild,
} from "@/features/builds";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { BuildLogViewer } from "./BuildLogViewer";
import { CommitRef, CompareLink, Dot, LiveDuration, triggerLabel } from "./deployParts";
import { CopyButton } from "@/components/ui/copy-button";

export type { BuildRowStatus };
export { formatDuration, triggerLabel } from "./deployParts";

const STATUS: Record<
  BuildRowStatus,
  { label: string; icon: React.ComponentType<{ className?: string }>; tone: string; badge?: string }
> = {
  active: { label: "Live", icon: CheckCircle2, tone: "text-[var(--success)]", badge: "border-[var(--success)]/40 bg-[var(--success-subtle)] text-[var(--success)]" },
  superseded: { label: "Succeeded", icon: CheckCircle2, tone: "text-[var(--text-tertiary)]" },
  failed: { label: "Failed", icon: XCircle, tone: "text-[var(--error)]", badge: "border-[var(--error)]/40 bg-[var(--error-subtle)] text-[var(--error)]" },
  "release-failed": { label: "Release failed", icon: AlertTriangle, tone: "text-[var(--warning)]", badge: "border-[var(--warning)]/40 bg-[var(--warning-subtle)] text-[var(--warning)]" },
  running: { label: "Building", icon: Loader2, tone: "text-[var(--building)] animate-spin", badge: "border-[var(--building)]/40 bg-[var(--building-subtle)] text-[var(--building)]" },
  pending: { label: "Starting", icon: CircleDashed, tone: "text-[var(--building)]", badge: "border-[var(--border-default)] text-[var(--text-secondary)]" },
  queued: { label: "Queued", icon: Clock, tone: "text-[var(--text-secondary)]", badge: "border-dashed border-[var(--border-default)] text-[var(--text-secondary)]" },
  cancelled: { label: "Cancelled", icon: Ban, tone: "text-[var(--text-tertiary)]", badge: "border-[var(--border-subtle)] text-[var(--text-tertiary)]" },
  unknown: { label: "Unknown", icon: CircleDashed, tone: "text-[var(--text-tertiary)]" },
};

export function StatusBadge({ s, queuePos }: { s: BuildRowStatus; queuePos?: number }) {
  const m = STATUS[s];
  if (!m.badge) return null;
  return (
    <span className={cn("inline-flex shrink-0 items-center rounded border px-1.5 py-px text-xs font-medium", m.badge)}>
      {s === "queued" && queuePos ? `Queued · #${queuePos}` : m.label}
    </span>
  );
}

export interface BuildRowProps {
  project: string;
  service: string;
  // env-group short name so rollback scopes to the right env CR.
  env: string;
  build: DeployBuild;
  // The older successful build on the same branch (compare base).
  previous?: DeployBuild;
  // The build is newer than what's live (the env was rolled back past it).
  newerThanLive?: boolean;
  repoUrl?: string;
  status: BuildRowStatus;
  duration: string;
  runningSince?: string;
  isOpen: boolean;
  canDeploy: boolean;
  onToggle: () => void;
}

export function BuildRow({
  project,
  service,
  env,
  build: b,
  previous,
  newerThanLive,
  repoUrl,
  status: s,
  duration,
  runningSince,
  isOpen,
  canDeploy,
  onToggle,
}: BuildRowProps) {
  const m = STATUS[s];
  const Icon = m.icon;
  const sha = buildRefLabel(b, 7);
  const ts = b.startedAt ?? b.finishedAt;
  const note = buildNote(b, s);
  const who = triggerLabel(b);
  const live = s === "running" || s === "pending" || s === "queued";
  const failed = s === "failed";
  const compareHref = githubCompareUrl(repoUrl, previous?.commitSha, b.commitSha);
  const logId = `build-log-${b.id}`;

  return (
    <li
      className={cn(
        "overflow-hidden rounded-lg border bg-[var(--bg-secondary)] transition-colors",
        failed ? "border-[var(--error)]/35" : s === "running" ? "border-[var(--building)]/35" : "border-[var(--border-subtle)]",
      )}
    >
      <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 px-3 py-2.5 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:px-4">
        <Icon aria-hidden className={cn("mt-0.5 h-4 w-4 shrink-0", m.tone)} />
        <div className="min-w-0">
          <div className="flex items-start gap-2">
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={isOpen}
              aria-controls={logId}
              className="min-w-0 flex-1 text-left text-sm font-medium text-[var(--text-primary)] hover:underline hover:underline-offset-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] rounded-sm"
            >
              <span className="line-clamp-2 break-words">
                {b.commitMessage || (b.triggeredBy === "user" ? "Manual redeploy" : sha ? `Deploy ${sha}` : "Deploy")}
              </span>
              <span className="sr-only">{`, ${m.label}. ${isOpen ? "Hide" : "Show"} build log`}</span>
            </button>
            <StatusBadge s={s} queuePos={b.queuePosition} />
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-[var(--text-secondary)]">
            <CommitRef build={b} repoUrl={repoUrl} />
            <Dot />
            <span className="max-w-[16ch] truncate" title={b.branch}>
              {b.branch ?? "—"}
            </span>
            {who && (
              <>
                <Dot />
                <span>{who}</span>
              </>
            )}
            <Dot />
            <time dateTime={ts} title={ts ? new Date(ts).toLocaleString() : undefined}>
              {ts ? relativeTime(ts) : "—"}
            </time>
            {runningSince ? (
              <>
                <Dot />
                <LiveDuration since={runningSince} />
              </>
            ) : duration ? (
              <>
                <Dot />
                <span className="tabular-nums" title="Build time">
                  {duration}
                </span>
              </>
            ) : null}
            {compareHref && (
              <span className="hidden sm:contents">
                <Dot />
                <CompareLink repoUrl={repoUrl} from={previous} to={b} />
              </span>
            )}
          </div>
          {b.promoteHold && (s === "running" || s === "pending") && (
            <p className="mt-1 text-xs text-[var(--warning)]">Promotion held: {b.promoteHold}</p>
          )}
          {s === "release-failed" && (
            <p className="mt-1 text-xs text-[var(--warning)]">
              The image built, but its release command failed. The previous version is still live.
            </p>
          )}
          {failed && (b.failureClass?.summary || b.errorMessage) && (
            <p className="mt-1 break-words text-xs text-[var(--error)]">{b.failureClass?.summary || b.errorMessage}</p>
          )}
          {note && <p className="mt-1 break-words text-xs text-[var(--text-tertiary)]">{note}</p>}
        </div>
        <div className="col-start-2 flex items-center gap-1 sm:col-start-3 sm:row-start-1 sm:self-start">
          <PrimaryAction project={project} service={service} env={env} build={b} status={s} sha={sha} canDeploy={canDeploy} newerThanLive={newerThanLive} />
          <RowMenu build={b} repoUrl={repoUrl} previous={previous} compareHref={compareHref} sha={sha} onToggleLog={onToggle} isOpen={isOpen} />
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={isOpen}
            aria-controls={logId}
            aria-label={isOpen ? "Hide build log" : "Show build log"}
            className="inline-flex h-7 w-7 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <ChevronDown className={cn("h-4 w-4 transition-transform", isOpen && "rotate-180")} />
          </button>
        </div>
      </div>
      {isOpen && (
        <div id={logId} className="min-w-0 border-t border-[var(--border-subtle)]">
          {s === "release-failed" ? (
            <>
              <ReleaseFailure message={b.errorMessage} job={b.releaseJob} logTail={b.releaseLogTail} />
              {!b.releaseLogTail && (
                <BuildLogViewer project={project} service={service} buildId={b.id} live={false} failed={false} />
              )}
            </>
          ) : (
            <>
              {failed && <Remediation failureClass={b.failureClass} />}
              <BuildLogViewer project={project} service={service} buildId={b.id} live={live} failed={failed} />
            </>
          )}
        </div>
      )}
    </li>
  );
}

// PrimaryAction is the one inline action that matters for the row's state.
function PrimaryAction({
  project,
  service,
  env,
  build: b,
  status: s,
  sha,
  canDeploy,
  newerThanLive,
}: {
  project: string;
  service: string;
  env: string;
  build: DeployBuild;
  status: BuildRowStatus;
  sha: string;
  canDeploy: boolean;
  newerThanLive?: boolean;
}) {
  if (!canDeploy) return null;
  if (s === "superseded") {
    if (!b.imageTag) {
      return (
        <span
          tabIndex={0}
          title="This build's image was removed by the registry retention sweep, so there's nothing to roll back to. Redeploy the commit instead."
          className="inline-flex h-7 cursor-help items-center gap-1 rounded-md border border-dashed border-[var(--border-subtle)] px-2 text-xs text-[var(--text-tertiary)]"
        >
          Image pruned
        </span>
      );
    }
    return (
      <RollbackButton
        project={project}
        service={service}
        env={env}
        buildId={b.id}
        sha={sha}
        branch={b.branch}
        label={newerThanLive ? "Roll forward" : "Rollback"}
      />
    );
  }
  if (s === "release-failed") return <RetryReleaseButton project={project} service={service} buildId={b.id} sha={sha} />;
  if (s === "running" || s === "pending" || s === "queued") return <CancelButton project={project} service={service} buildId={b.id} />;
  if (s === "failed" || s === "cancelled") {
    return <RetryBuildButton project={project} service={service} build={b} sha={sha} />;
  }
  return null;
}

const ACTION_BTN =
  "inline-flex h-7 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 text-xs text-[var(--text-secondary)] transition-colors hover:border-[var(--border-strong)] hover:text-[var(--text-primary)] disabled:opacity-50";

// RowMenu holds the secondary actions. On phones it is also the only home for
// compare (the meta line drops it to save width).
function RowMenu({
  build: b,
  repoUrl,
  previous,
  compareHref,
  sha,
  onToggleLog,
  isOpen,
}: {
  build: DeployBuild;
  repoUrl?: string;
  previous?: DeployBuild;
  compareHref: string | null;
  sha: string;
  onToggleLog: () => void;
  isOpen: boolean;
}) {
  const [open, setOpen] = useState(false);
  const commitHref = githubCommitUrl(repoUrl, b.commitSha);
  const item =
    "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]";
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label="More actions"
        className="inline-flex h-7 w-7 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        <MoreHorizontal className="h-4 w-4" />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-56 gap-0.5 rounded-lg p-1">
        <button
          type="button"
          className={item}
          onClick={() => {
            onToggleLog();
            setOpen(false);
          }}
        >
          {isOpen ? "Hide build log" : "Show build log"}
        </button>
        {compareHref && (
          <a className={item} href={compareHref} target="_blank" rel="noreferrer" onClick={() => setOpen(false)}>
            Compare with {previous ? buildRefLabel(previous, 7) : "previous"}
          </a>
        )}
        {commitHref && (
          <a
            className={item}
            href={commitHref}
            target="_blank"
            rel="noreferrer"
            onClick={() => setOpen(false)}
          >
            View commit
          </a>
        )}
        <div className={cn(item, "justify-between")}>
          <span>Copy {b.commitSha ? "commit SHA" : "ref"}</span>
          <CopyButton value={b.commitSha || b.ref || sha} label="Copy commit SHA" />
        </div>
        <div className={cn(item, "justify-between")}>
          <span>Copy build ID</span>
          <CopyButton value={b.id} label="Copy build ID" />
        </div>
      </PopoverContent>
    </Popover>
  );
}

// Remediation renders the classifier's fix for a failed build above the log.
function Remediation({ failureClass }: { failureClass?: BuildFailureClass }) {
  const rem = failureClass?.remediation;
  if (!failureClass?.lineHint && !rem) return null;
  return (
    <div className="space-y-2 border-b border-[var(--border-subtle)] bg-[var(--error-subtle)] px-3 py-2.5 sm:px-4">
      {failureClass?.lineHint && (
        <p className="break-words font-mono text-xs text-[var(--text-secondary)]">
          {failureClass.lineNum ? `line ${failureClass.lineNum}: ` : ""}
          {failureClass.lineHint}
        </p>
      )}
      {rem && (
        <div>
          <p className="text-sm font-medium text-[var(--text-primary)]">{rem.title}</p>
          {rem.detail && <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">{rem.detail}</p>}
          {rem.fix && (
            <div className="mt-2 overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
              <div className="flex items-center justify-between border-b border-[var(--border-subtle)] px-2 py-1">
                <span className="text-xs text-[var(--text-tertiary)]">{rem.fixLang || "Fix"}</span>
                <CopyButton value={rem.fix} label="Copy" showLabel />
              </div>
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words p-2 font-mono text-xs text-[var(--text-secondary)]">
                {rem.fix}
              </pre>
            </div>
          )}
          {rem.docsAnchor && (
            <a
              href={rem.docsAnchor}
              target="_blank"
              rel="noreferrer"
              className="mt-1.5 inline-block text-xs text-[var(--text-secondary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
            >
              Read the docs
            </a>
          )}
        </div>
      )}
    </div>
  );
}

function ReleaseFailure({ message, job, logTail }: { message?: string; job?: string; logTail?: string }) {
  return (
    <div className="space-y-2 border-b border-[var(--border-subtle)] bg-[var(--warning-subtle)] px-3 py-2.5 sm:px-4">
      <p className="text-sm font-medium text-[var(--text-primary)]">
        Release command failed{job ? <span className="ml-1 font-mono text-xs font-normal text-[var(--text-tertiary)]">{job}</span> : null}
      </p>
      {message && <p className="break-words font-mono text-xs text-[var(--text-secondary)]">{message}</p>}
      {logTail && (
        <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-[var(--console-bg)] p-2 font-mono text-xs text-[var(--console-fg)]">
          {logTail}
        </pre>
      )}
    </div>
  );
}

function CancelButton({ project, service, buildId }: { project: string; service: string; buildId: string }) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: () => cancelBuild(project, service, buildId),
    onSuccess: () => {
      toast.success("Build cancelled");
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "builds"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Cancel failed"),
  });
  return (
    <button
      type="button"
      onClick={() => m.mutate()}
      disabled={m.isPending}
      title="Stop this build. Nothing changes on the live version."
      className={cn(ACTION_BTN, "hover:border-[var(--error)]/50 hover:text-[var(--error)]")}
    >
      <X className="h-3.5 w-3.5" />
      <span>{m.isPending ? "Cancelling…" : "Cancel"}</span>
    </button>
  );
}

// RetryBuildButton rebuilds the same commit (failed or cancelled build).
function RetryBuildButton({ project, service, build: b, sha }: { project: string; service: string; build: DeployBuild; sha: string }) {
  const [open, setOpen] = useState(false);
  const trigger = useTriggerBuild(project, service);
  const body = { branch: b.branch, ...(b.commitSha ? { ref: b.commitSha } : {}) };
  const run = async () => {
    try {
      const res = await trigger.mutateAsync(body);
      toast.success(buildTriggerMessage(res, "Rebuild started"));
      setOpen(false);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to start the build");
    }
  };
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} disabled={trigger.isPending} className={ACTION_BTN} title="Build this commit again">
        <RotateCcw className="h-3.5 w-3.5" />
        <span>Retry</span>
      </button>
      <ConfirmDialog
        open={open}
        title="Retry this build?"
        destructive={false}
        confirmLabel="Rebuild"
        body={
          <span>
            Builds <span className="font-mono text-[var(--text-primary)]">{b.commitSha ? sha : "the latest commit"}</span> on{" "}
            <span className="font-mono text-[var(--text-primary)]">{b.branch || "the default branch"}</span> again and
            deploys it if it succeeds.
          </span>
        }
        pending={trigger.isPending}
        onConfirm={run}
        onCancel={() => setOpen(false)}
      />
    </>
  );
}

function RetryReleaseButton({ project, service, buildId, sha }: { project: string; service: string; buildId: string; sha: string }) {
  const [open, setOpen] = useState(false);
  const m = useRetryRelease(project, service);
  const run = () =>
    m.mutate(buildId, {
      onSuccess: (res) => toast.success(res.job ? `Release retry started (${res.job})` : "Release retry started"),
      onError: (e) => toast.error(e instanceof Error ? e.message : "Retry failed"),
      onSettled: () => setOpen(false),
    });
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} disabled={m.isPending} title="Re-run the release command for this build" className={ACTION_BTN}>
        <RefreshCw className="h-3.5 w-3.5" />
        <span>Retry release</span>
      </button>
      <ConfirmDialog
        open={open}
        title={`Retry release for ${sha || buildId}?`}
        destructive={false}
        confirmLabel="Retry release"
        body="Runs the release command (e.g. migrations) again. The build goes live if it succeeds."
        pending={m.isPending}
        onConfirm={run}
        onCancel={() => setOpen(false)}
      />
    </>
  );
}

export function RollbackButton({
  project,
  service,
  env,
  buildId,
  sha,
  branch,
  label = "Rollback",
}: {
  project: string;
  service: string;
  env: string;
  buildId: string;
  sha: string;
  branch?: string;
  label?: string;
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
          toast.success(`${target} now runs ${sha || buildId}`);
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
        onClick={() => setStep("confirm")}
        title={`Point ${target} at ${sha || buildId}`}
        className={cn(ACTION_BTN, "hover:border-[var(--warning)]/50 hover:text-[var(--warning)]")}
      >
        <Undo2 className="h-3.5 w-3.5" />
        <span>{label}</span>
      </button>
      <ConfirmDialog
        open={step === "confirm"}
        title={label === "Rollback" ? `Roll ${target} back to ${sha || buildId}?` : `Switch ${target} to ${sha || buildId}?`}
        destructive={false}
        confirmLabel={label === "Rollback" ? "Roll back" : "Switch"}
        body={
          <span>
            <strong>{target}</strong> switches to this build&apos;s image right away, no rebuild. Environment variables stay as
            they are now. The next push to <span className="font-mono">{branch || "the branch"}</span> deploys over it.
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
