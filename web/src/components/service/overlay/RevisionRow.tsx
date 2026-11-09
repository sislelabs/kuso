"use client";

import { useState } from "react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { ChevronDown, Undo2 } from "lucide-react";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { describeRevision, useRevertRevision, type Revision, type RolloutOutcome } from "@/features/revisions";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { RevisionDiff } from "./RevisionChanges";

// Config changes hang off a thin rail aligned with the build rows' status
// icons, so builds read as the primary items. The rail runs through the
// list gap (space-y-2) to meet the neighbouring rows.
const RAIL =
  "relative pl-9 sm:pl-10 before:absolute before:-top-2 before:-bottom-2 before:left-[21px] before:w-px before:bg-[var(--border-default)] sm:before:left-[25px]";

function RailDot({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "absolute left-[21px] top-1/2 h-1.5 w-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-[var(--text-tertiary)] ring-2 ring-[var(--bg-primary)] sm:left-[25px]",
        className,
      )}
    />
  );
}

const OUTCOME_TONE: Record<RolloutOutcome["tone"], string> = {
  ok: "text-[var(--success)]",
  warn: "text-[var(--warning)]",
  progress: "text-[var(--building)]",
};

// OutcomeChip: what the pods did with a config change (rolled out,
// rolling, failing), so a config-only rollout reads as a deploy.
export function OutcomeChip({ outcome }: { outcome: RolloutOutcome }) {
  return (
    <span className={cn("shrink-0 font-medium", OUTCOME_TONE[outcome.tone])}>
      {outcome.label}
      {outcome.at ? (
        <time dateTime={outcome.at} title={new Date(outcome.at).toLocaleString()} className="font-normal text-[var(--text-tertiary)]">
          {" "}
          {relativeTime(outcome.at)}
        </time>
      ) : null}
    </span>
  );
}

// RevisionRow is a compact config-change entry interleaved with builds.
export function RevisionRow({
  project,
  revision,
  canRevert,
  history,
  outcome,
}: {
  project: string;
  revision: Revision;
  canRevert: boolean;
  // history: the revision list for the same resource, used to show old → new.
  history?: readonly Revision[];
  // Pod outcome when this change triggered the latest rollout.
  outcome?: RolloutOutcome;
}) {
  const [open, setOpen] = useState(false);
  const [showDiff, setShowDiff] = useState(false);
  const revert = useRevertRevision(project);
  const v = describeRevision(revision);
  const meta = [
    v.detail,
    revision.actor ? `by ${revision.actor}` : "",
    relativeTime(revision.createdAt),
  ].filter(Boolean);
  const onConfirm = () =>
    revert.mutate(revision.id, {
      onSuccess: () => toast.success("Reverted"),
      onError: (e) => toastError(e, "Revert failed"),
      onSettled: () => setOpen(false),
    });
  return (
    <li>
    <div className={cn(RAIL, "group/rev flex min-h-7 items-center gap-3 py-0.5 pr-3 text-xs text-[var(--text-tertiary)] sm:pr-4")}>
      <RailDot />
      <span className="min-w-0 truncate" title={revision.summary}>
        <span className="text-[var(--text-secondary)]">{v.label}</span>
        {meta.map((m, i) => (
          <span key={i}>
            {" · "}
            {m}
          </span>
        ))}
      </span>
      {outcome && <OutcomeChip outcome={outcome} />}
      {canRevert && v.revertable && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          disabled={revert.isPending}
          title="Restore the config from this change"
          className="ml-auto inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-xs text-[var(--text-secondary)] opacity-0 transition-opacity hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:opacity-100 group-hover/rev:opacity-100 disabled:opacity-50 [@media(hover:none)]:opacity-100"
        >
          <Undo2 className="h-3 w-3" />
          Revert
        </button>
      )}
      {history && (
        <button
          type="button"
          aria-expanded={showDiff}
          onClick={() => setShowDiff((d) => !d)}
          className={cn(
            "inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
            !(canRevert && v.revertable) && "ml-auto",
          )}
        >
          Changes
          <ChevronDown aria-hidden className={`h-3 w-3 transition-transform ${showDiff ? "rotate-180" : ""}`} />
        </button>
      )}
    </div>
    {showDiff && history && (
      <div className="ml-8 mb-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <RevisionDiff revision={revision} history={history} />
      </div>
    )}
      <ConfirmDialog
        open={open}
        title={`Revert: ${v.label.toLowerCase()}?`}
        destructive={false}
        confirmLabel="Revert"
        body={
          <span>
            Restores the config as it was after this change
            {revision.summary ? (
              <>
                {" "}(<span className="font-mono">{revision.summary}</span>)
              </>
            ) : null}
            .
          </span>
        }
        pending={revert.isPending}
        onConfirm={onConfirm}
        onCancel={() => setOpen(false)}
      />
    </li>
  );
}

// RevisionGroup folds a run of consecutive config changes into one line so
// a burst of settings edits doesn't push the builds off screen.
export function RevisionGroup({
  project,
  revisions,
  canRevert,
  outcome,
}: {
  project: string;
  revisions: Revision[];
  canRevert: boolean;
  // The latest rollout's outcome, when one of these changes triggered it.
  outcome?: { id: string; outcome: RolloutOutcome } | null;
}) {
  const inGroup = outcome && revisions.some((r) => r.id === outcome.id) ? outcome : null;
  const [open, setOpen] = useState(false);
  const newest = revisions[0];
  const oldest = revisions[revisions.length - 1];
  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className={cn(
          RAIL,
          "flex min-h-7 w-full items-center gap-3 rounded-md py-0.5 pr-3 text-left text-xs text-[var(--text-tertiary)] hover:text-[var(--text-secondary)] sm:pr-4",
        )}
      >
        <RailDot className="h-2 w-2 bg-[var(--bg-primary)] ring-1 ring-[var(--text-tertiary)]" />
        <span className="min-w-0 flex-1 truncate">
          <span className="text-[var(--text-secondary)]">{revisions.length} config changes</span>
          {" · "}
          {relativeTime(oldest.createdAt) === relativeTime(newest.createdAt)
            ? relativeTime(newest.createdAt)
            : `${relativeTime(oldest.createdAt)} – ${relativeTime(newest.createdAt)}`}
        </span>
        {inGroup && <OutcomeChip outcome={inGroup.outcome} />}
        <ChevronDown aria-hidden className={`h-3.5 w-3.5 shrink-0 transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <ul>
          {revisions.map((r) => (
            <RevisionRow
              key={r.id}
              project={project}
              revision={r}
              canRevert={canRevert}
              history={revisions}
              outcome={inGroup?.id === r.id ? inGroup.outcome : undefined}
            />
          ))}
        </ul>
      )}
    </li>
  );
}
