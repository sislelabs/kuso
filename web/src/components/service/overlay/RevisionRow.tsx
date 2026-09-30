"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Settings2, Undo2 } from "lucide-react";
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
    <li className="flex items-center gap-2 px-3 py-1 text-xs text-[var(--text-tertiary)]">
      <Settings2 className="h-3 w-3 shrink-0" />
      <span className="min-w-0 truncate" title={revision.summary}>
        <span className="text-[var(--text-secondary)]">{v.label}</span>
        {meta.map((m, i) => (
          <span key={i} className="font-mono text-[10px]">
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
          className="ml-auto inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 font-mono text-[10px] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
        >
          <Undo2 className="h-3 w-3" />
          revert
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
