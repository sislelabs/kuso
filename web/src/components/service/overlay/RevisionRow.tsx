"use client";

import { useState } from "react";
import { toast } from "sonner";
import { ChevronDown, Settings2, Undo2 } from "lucide-react";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { describeRevision, useRevertRevision, type Revision } from "@/features/revisions";
import { relativeTime } from "@/lib/format";

// RevisionRow is a compact config-change entry interleaved with builds.
export function RevisionRow({
  project,
  revision,
  canRevert,
}: {
  project: string;
  revision: Revision;
  canRevert: boolean;
}) {
  const [open, setOpen] = useState(false);
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
      onError: (e) => toast.error(e instanceof Error ? e.message : "Revert failed"),
      onSettled: () => setOpen(false),
    });
  return (
    <li className="flex min-h-8 items-center gap-3 px-3 py-1 text-xs text-[var(--text-tertiary)] sm:px-4">
      <Settings2 aria-hidden className="h-3.5 w-3.5 shrink-0" />
      <span className="min-w-0 truncate" title={revision.summary}>
        <span className="text-[var(--text-secondary)]">{v.label}</span>
        {meta.map((m, i) => (
          <span key={i}>
            {" · "}
            {m}
          </span>
        ))}
      </span>
      {canRevert && v.revertable && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          disabled={revert.isPending}
          title="Restore the config from this change"
          className="ml-auto inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
        >
          <Undo2 className="h-3 w-3" />
          Revert
        </button>
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
}: {
  project: string;
  revisions: Revision[];
  canRevert: boolean;
}) {
  const [open, setOpen] = useState(false);
  const newest = revisions[0];
  const oldest = revisions[revisions.length - 1];
  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-8 w-full items-center gap-3 rounded-md px-3 py-1 text-left text-xs text-[var(--text-tertiary)] hover:bg-[var(--bg-secondary)] sm:px-4"
      >
        <Settings2 aria-hidden className="h-3.5 w-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate">
          <span className="text-[var(--text-secondary)]">{revisions.length} config changes</span>
          {" · "}
          {relativeTime(oldest.createdAt) === relativeTime(newest.createdAt)
            ? relativeTime(newest.createdAt)
            : `${relativeTime(oldest.createdAt)} – ${relativeTime(newest.createdAt)}`}
        </span>
        <ChevronDown aria-hidden className={`h-3.5 w-3.5 shrink-0 transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <ul className="ml-[1.4rem] border-l border-[var(--border-subtle)]">
          {revisions.map((r) => (
            <RevisionRow key={r.id} project={project} revision={r} canRevert={canRevert} />
          ))}
        </ul>
      )}
    </li>
  );
}
