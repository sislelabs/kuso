"use client";

// One deploy-history row: status, commit, meta, the row's actions, and the
// expandable build log (failure summary first for failed builds).

import { smoothScroll } from "@/lib/reduced-motion";
import { useMemo, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Ban,
  CheckCircle2,
  ChevronDown,
  CircleDashed,
  Clock,
  GitBranch,
  Loader2,
  MoreHorizontal,
  RefreshCw,
  RotateCcw,
  Undo2,
  X,
  XCircle,
} from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { buildTriggerMessage, cancelBuild, useTriggerBuild } from "@/features/services";
import type { BuildFailureClass } from "@/features/services/api";
import {
  buildNote,
  buildRefLabel,
  failedStepFromBuild,
  failedStepTitle,
  firstErrorLine,
  isErrorLine,
  parseBuildLog,
  useStoredBuildLog,
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
import { Button } from "@/components/ui/button";

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
  // What's live now: the rollback dialog compares against it.
  liveBuild?: DeployBuild;
  // Fetch the stored log of a failed build to show its first error line.
  peekLog?: boolean;
  repoUrl?: string;
  status: BuildRowStatus;
  duration: string;
  runningSince?: string;
  isOpen: boolean;
  canDeploy: boolean;
  onToggle: () => void;
  // Jump to another overlay tab (Settings → Source for a missing branch).
  onNavigate?: (tab: string, anchor?: string) => void;
  // The newest row you can roll back to keeps its actions visible, so
  // "how do I undo this deploy" is answered without hovering.
  pinActions?: boolean;
}

export function BuildRow({
  project,
  service,
  env,
  build: b,
  previous,
  newerThanLive,
  liveBuild,
  peekLog,
  repoUrl,
  status: s,
  duration,
  runningSince,
  isOpen,
  canDeploy,
  onToggle,
  onNavigate,
  pinActions = false,
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
  const rowRef = useRef<HTMLLIElement>(null);
  const peek = useLogPeek(project, service, b.id, failed && !!peekLog);
  const failedStep = peek.step ?? failedStepFromBuild(b);
  const errorLine = b.failureClass?.lineHint || peek.errorLine;
  // The classifier's "generic" summary says less than the step and error line.
  const classSummary = b.failureClass?.kind === "generic" && (failedStep || errorLine) ? "" : b.failureClass?.summary;
  const failureTitle = classSummary || (isGenericFailure(b.errorMessage) ? "" : b.errorMessage);
  const releaseErrorLine = s === "release-failed" ? firstReleaseError(b) : undefined;
  // Settled rows keep their actions out of the way until hovered or
  // focused; touch screens have no hover, so they always show them there.
  const quietActions = !pinActions && (s === "superseded" || s === "active" || s === "cancelled" || s === "unknown");
  const viewFailure = () => {
    if (!isOpen) onToggle();
    requestAnimationFrame(() => rowRef.current?.scrollIntoView({ block: "start", behavior: smoothScroll() }));
  };

  return (
    <li
      ref={rowRef}
      id={`build-row-${b.id}`}
      data-build-row
      className={cn(
        "group scroll-mt-2 overflow-hidden rounded-lg border bg-[var(--bg-secondary)] transition-colors",
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
                {buildTitle(b, sha)}
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
            <div className="mt-1.5 min-w-0 space-y-1">
              <p className="text-xs text-[var(--warning)]">
                <span className="font-medium">Failed at release command</span>
                <span className="text-[var(--text-secondary)]"> · the image built, the previous version is still live</span>
              </p>
              {releaseErrorLine && (
                <p
                  className="rounded border-l-2 border-[var(--warning)] bg-[var(--warning-subtle)] px-2 py-1 font-mono text-xs text-[var(--text-primary)]"
                  title={releaseErrorLine}
                >
                  <span className="line-clamp-2 break-words">{releaseErrorLine}</span>
                </p>
              )}
            </div>
          )}
          {failed && (failedStep || failureTitle || errorLine || b.errorMessage) && (
            <div className="mt-1.5 min-w-0 space-y-1">
              {(failedStep || failureTitle) && (
                <p className="break-words text-xs text-[var(--error)]">
                  {failedStep && (
                    <span className="font-medium">
                      Failed at <span className="text-[var(--text-primary)]">{failedStep}</span>
                    </span>
                  )}
                  {failedStep && failureTitle && <span aria-hidden> · </span>}
                  {failureTitle && <span className={failedStep ? undefined : "font-medium"}>{failureTitle}</span>}
                </p>
              )}
              {errorLine ? (
                <p
                  className="rounded border-l-2 border-[var(--error)] bg-[var(--error-subtle)] px-2 py-1 font-mono text-xs text-[var(--text-primary)]"
                  title={errorLine}
                >
                  <span className="line-clamp-2 break-words">{errorLine}</span>
                </p>
              ) : (
                !failureTitle && b.errorMessage && !(failedStep && isGenericFailure(b.errorMessage)) && <p className="break-words text-xs text-[var(--error)]">{b.errorMessage}</p>
              )}
              {!isOpen && (
                <button
                  type="button"
                  onClick={viewFailure}
                  aria-controls={logId}
                  className="text-xs font-medium text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--link)]"
                >
                  View failure
                </button>
              )}
            </div>
          )}
          {note && <p className="mt-1 break-words text-xs text-[var(--text-tertiary)]">{note}</p>}
        </div>
        <div
          className={cn(
            "col-start-2 flex items-center gap-1 sm:col-start-3 sm:row-start-1 sm:self-start",
            quietActions &&
              !isOpen &&
              "opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 has-[[data-popup-open]]:opacity-100 [@media(hover:none)]:opacity-100",
          )}
        >
          <PrimaryAction
            project={project}
            service={service}
            env={env}
            build={b}
            status={s}
            sha={sha}
            canDeploy={canDeploy}
            newerThanLive={newerThanLive}
            liveBuild={liveBuild}
            repoUrl={repoUrl}
            onNavigate={onNavigate}
          />
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
  liveBuild,
  repoUrl,
  onNavigate,
}: {
  project: string;
  service: string;
  env: string;
  build: DeployBuild;
  status: BuildRowStatus;
  sha: string;
  canDeploy: boolean;
  newerThanLive?: boolean;
  liveBuild?: DeployBuild;
  repoUrl?: string;
  onNavigate?: (tab: string, anchor?: string) => void;
}) {
  if (!canDeploy) return null;
  if (s === "superseded") {
    if (!b.imageTag) {
      // The retention sweep removed the image; a real SHA can be built
      // again, a synthetic ref can't be pinned to a commit.
      if (b.commitSha && FULL_SHA.test(b.commitSha)) {
        return <RetryBuildButton project={project} service={service} build={b} sha={sha} pruned />;
      }
      return (
        <span
          tabIndex={0}
          title="This build's image was removed by the registry retention sweep and it has no commit SHA to rebuild. Redeploy the branch instead."
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
        build={b}
        liveBuild={liveBuild}
        repoUrl={repoUrl}
        label={newerThanLive ? "Roll forward" : "Rollback"}
      />
    );
  }
  if (s === "release-failed") return <RetryReleaseButton project={project} service={service} buildId={b.id} sha={sha} />;
  if (s === "running" || s === "pending" || s === "queued") return <CancelButton project={project} service={service} buildId={b.id} />;
  if (s === "failed" || s === "cancelled") {
    const stuck = s === "failed" ? unretryableReason(b) : undefined;
    const missingBranch = stuck && isMissingBranch(b);
    return (
      <>
        {missingBranch && onNavigate && (
          <button type="button" onClick={() => onNavigate("settings", "source")} className={ACTION_BTN}>
            <GitBranch className="h-3.5 w-3.5" />
            <span>Change branch</span>
          </button>
        )}
        <RetryBuildButton project={project} service={service} build={b} sha={sha} blockedReason={stuck} />
      </>
    );
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
    onError: (e) => toastError(e, "Cancel failed"),
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

// Failures a retry of the same ref can't clear: the branch is gone, or
// the commit itself is broken. Matched on the classifier kind, and on the
// raw git message for builds classified before clone_ref_missing existed.
function isMissingBranch(b: DeployBuild): boolean {
  return (
    b.failureClass?.kind === "clone_ref_missing" ||
    /Remote branch \S+ not found|couldn't find remote ref/i.test(`${b.errorMessage ?? ""} ${b.failureClass?.lineHint ?? ""}`)
  );
}

export function unretryableReason(b: DeployBuild): string | undefined {
  if (isMissingBranch(b)) {
    return `${b.branch ? `Branch ${b.branch}` : "This branch"} doesn't exist in the repository any more, so a retry fails the same way. Change the branch or push it again.`;
  }
  switch (b.failureClass?.kind) {
    case "lockfile_drift":
      return "The lockfile in this commit is out of sync with package.json; rebuilding the same commit fails the same way. Push a commit with an updated lockfile.";
    case "dockerfile_missing_copy":
      return "The Dockerfile copies a file this commit doesn't have; rebuilding it fails the same way. Push a fix.";
    default:
      return undefined;
  }
}

// RetryBuildButton rebuilds the same commit: a failed or cancelled build,
// or (pruned) a succeeded one whose image the retention sweep removed.
function RetryBuildButton({
  project,
  service,
  build: b,
  sha,
  pruned,
  blockedReason,
}: {
  project: string;
  service: string;
  build: DeployBuild;
  sha: string;
  pruned?: boolean;
  // Set when a retry can't succeed: the button stays visible but disabled
  // with this as its reason.
  blockedReason?: string;
}) {
  const [open, setOpen] = useState(false);
  const trigger = useTriggerBuild(project, service);
  if (blockedReason) {
    return (
      <Button variant="outline" size="xs" disabled disabledReason={blockedReason} className="h-7 text-[var(--text-tertiary)]">
        <RotateCcw className="h-3.5 w-3.5" />
        Retry
      </Button>
    );
  }
  const body = { branch: b.branch, ...(b.commitSha ? { ref: b.commitSha } : {}) };
  const run = async () => {
    try {
      const res = await trigger.mutateAsync(body);
      toast.success(buildTriggerMessage(res, "Rebuild started"));
      setOpen(false);
    } catch (e) {
      toastError(e, "Failed to start the build");
    }
  };
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        disabled={trigger.isPending}
        className={ACTION_BTN}
        title={
          pruned
            ? "This build's image was removed by the registry retention sweep. Build the same commit again to get it back."
            : "Build this commit again"
        }
      >
        <RotateCcw className="h-3.5 w-3.5" />
        <span>{pruned ? "Rebuild" : "Retry"}</span>
      </button>
      <ConfirmDialog
        open={open}
        title={pruned ? `Rebuild ${sha}?` : "Retry this build?"}
        destructive={false}
        confirmLabel="Rebuild"
        body={
          <span className="space-y-2">
            {pruned && (
              <span className="block">
                Its image was removed by the registry retention sweep, so it can&apos;t be rolled back to directly.
              </span>
            )}
            <span className="block">
              Builds <span className="font-mono text-[var(--text-primary)]">{b.commitSha ? sha : "the latest commit"}</span> on{" "}
              <span className="font-mono text-[var(--text-primary)]">{b.branch || "the default branch"}</span> again and
              deploys it if it succeeds{pruned ? ", replacing what's live now" : ""}.
            </span>
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
      onError: (e) => toastError(e, "Retry failed"),
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
  build: b,
  liveBuild,
  repoUrl,
  label = "Rollback",
}: {
  project: string;
  service: string;
  env: string;
  build: DeployBuild;
  liveBuild?: DeployBuild;
  repoUrl?: string;
  label?: string;
}) {
  const [step, setStep] = useState<"idle" | "confirm" | "mismatch">("idle");
  const [mismatch, setMismatch] = useState("");
  const m = useRollbackToBuild(project, service, env);
  const target = env || "production";
  const sha = buildRefLabel(b, 7) || b.id;
  const back = label === "Rollback";
  const run = (force: boolean) =>
    m.mutate(
      { build: b.id, force },
      {
        onSuccess: () => {
          toast.success(`${target} now runs ${sha}`);
          setStep("idle");
        },
        onError: (e) => {
          if (!force && isBranchMismatch(e)) {
            setMismatch(e.message);
            setStep("mismatch");
            return;
          }
          toastError(e, "Rollback failed");
          setStep("idle");
        },
      },
    );
  return (
    <>
      <button
        type="button"
        onClick={() => setStep("confirm")}
        title={`Point ${target} at ${sha} right away, without rebuilding`}
        className={cn(ACTION_BTN, "hover:border-[var(--warning)]/50 hover:text-[var(--warning)]")}
      >
        <Undo2 className="h-3.5 w-3.5" />
        <span>{label}</span>
      </button>
      <ConfirmDialog
        open={step === "confirm"}
        title={back ? `Roll ${target} back to ${sha}?` : `Switch ${target} to ${sha}?`}
        destructive={false}
        confirmLabel={back ? "Roll back" : "Switch"}
        body={<RollbackSummary target={target} build={b} liveBuild={liveBuild} repoUrl={repoUrl} back={back} />}
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

// RollbackSummary names exactly what goes live and what it replaces, with
// a GitHub compare link for the commits in between.
function RollbackSummary({
  target,
  build: b,
  liveBuild,
  repoUrl,
  back,
}: {
  target: string;
  build: DeployBuild;
  liveBuild?: DeployBuild;
  repoUrl?: string;
  back: boolean;
}) {
  const ts = b.finishedAt ?? b.startedAt;
  const who = triggerLabel(b);
  const compare =
    liveBuild && liveBuild.id !== b.id
      ? back
        ? githubCompareUrl(repoUrl, b.commitSha, liveBuild.commitSha)
        : githubCompareUrl(repoUrl, liveBuild.commitSha, b.commitSha)
      : null;
  return (
    <span className="block space-y-3">
      <span className="block rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2">
        <span className="block break-words text-sm font-medium text-[var(--text-primary)]">
          {buildTitle(b, buildRefLabel(b, 7))}
        </span>
        <span className="mt-1 flex flex-wrap items-center gap-x-1.5 text-xs text-[var(--text-secondary)]">
          <span className="font-mono">{buildRefLabel(b, 7) || b.id}</span>
          {b.branch && (
            <>
              <Dot />
              <span>{b.branch}</span>
            </>
          )}
          {who && (
            <>
              <Dot />
              <span>{who}</span>
            </>
          )}
          {ts && (
            <>
              <Dot />
              <time dateTime={ts} title={new Date(ts).toLocaleString()}>
                built {relativeTime(ts)}
              </time>
            </>
          )}
        </span>
      </span>
      {liveBuild && liveBuild.id !== b.id && (
        <span className="block text-xs text-[var(--text-secondary)]">
          Replaces <span className="font-mono text-[var(--text-primary)]">{buildRefLabel(liveBuild, 7) || liveBuild.id}</span>
          {liveBuild.commitMessage ? <> ({liveBuild.commitMessage})</> : null}, which is live now.
          {compare && (
            <>
              {" "}
              <a href={compare} target="_blank" rel="noreferrer" className="text-[var(--link)] underline underline-offset-2">
                {back ? "See the commits being rolled back" : "See the commits being added"}
              </a>
            </>
          )}
        </span>
      )}
      <span className="block text-xs text-[var(--text-secondary)]">
        <strong className="text-[var(--text-primary)]">{target}</strong> switches to this build&apos;s image right away, with
        no rebuild. Variables stay as they are now. The next push to{" "}
        <span className="font-mono">{b.branch || "the branch"}</span> deploys over it.
      </span>
    </span>
  );
}

const FULL_SHA = /^[0-9a-f]{40}$/i;

function buildTitle(b: DeployBuild, sha: string): string {
  return b.commitMessage || (b.triggeredBy === "user" ? "Manual redeploy" : sha ? `Deploy ${sha}` : "Deploy");
}

// Server-side fallbacks that say a container failed without saying why;
// the log's own first error line is more useful than these.
function isGenericFailure(msg?: string): boolean {
  return !msg || /^container exited with code \d+|backoff limit|^Job has reached/i.test(msg);
}

// useLogPeek reads a failed build's stored log for the step it died in and
// its first descriptive error line.
function useLogPeek(
  project: string,
  service: string,
  buildId: string,
  enabled: boolean,
): { step?: string; errorLine?: string } {
  const log = useStoredBuildLog(project, service, buildId, enabled);
  return useMemo(() => {
    if (!log.data || log.data.length === 0) return {};
    const parsed = parseBuildLog(log.data.map((l) => l.line), { failed: true });
    return { step: failedStepTitle(parsed), errorLine: firstErrorLine(parsed) };
  }, [log.data]);
}

function firstReleaseError(b: DeployBuild): string | undefined {
  const lines = (b.releaseLogTail ?? "").split("\n").map((l) => l.trim()).filter(Boolean);
  return lines.find(isErrorLine) ?? (isGenericFailure(b.errorMessage) ? undefined : b.errorMessage);
}
