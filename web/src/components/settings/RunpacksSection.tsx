"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import {
  deleteRunpack,
  listRunpacks,
  runpacksQueryKey,
  type Runpack,
  type RunpackPhase,
} from "@/features/instance-config";

function phaseImage(p: RunpackPhase | null | undefined): string {
  if (!p) return "-";
  return p.Tag ? `${p.Repository}:${p.Tag}` : p.Repository;
}

// RunpacksSection lists the runpacks table. There is no create/edit API;
// delete is the only mutation (settings:admin).
export function RunpacksSection({ canEdit }: { canEdit: boolean }) {
  const qc = useQueryClient();
  const packs = useQuery({ queryKey: runpacksQueryKey, queryFn: listRunpacks });
  const [deleting, setDeleting] = useState<Runpack | null>(null);
  const remove = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (id: string) => deleteRunpack(id),
    onSuccess: () => {
      toast.success("Runpack deleted");
      void qc.invalidateQueries({ queryKey: runpacksQueryKey });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
    onSettled: () => setDeleting(null),
  });

  const rows = packs.data ?? [];

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="border-b border-[var(--border-subtle)] px-4 py-2.5">
        <h2 className="text-sm font-semibold tracking-tight">Runpacks</h2>
        <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
          Fetch / build / run image triples stored in the control-plane DB. Read-only apart from
          delete.
        </p>
      </header>
      {packs.isPending ? (
        <Skeleton className="m-3 h-12" />
      ) : packs.isError ? (
        <QueryErrorState what="runpacks" error={packs.error} onRetry={() => void packs.refetch()} className="m-3" />
      ) : rows.length === 0 ? (
        <p className="px-4 py-4 text-[11px] text-[var(--text-tertiary)]">No runpacks on this instance.</p>
      ) : (
        <ul>
          {rows.map((r) => (
            <li
              key={r.ID}
              className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-4 py-2.5 last:border-b-0"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-[12px] text-[var(--text-primary)]">{r.Name}</span>
                  {r.Language && (
                    <span className="rounded bg-[var(--bg-tertiary)] px-1.5 font-mono text-[10px] text-[var(--text-secondary)]">
                      {r.Language}
                    </span>
                  )}
                </div>
                <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
                  fetch {phaseImage(r.Fetch)} · build {phaseImage(r.Build)} · run {phaseImage(r.Run)}
                </p>
              </div>
              {canEdit && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Delete runpack ${r.Name}`}
                  onClick={() => setDeleting(r)}
                  disabled={remove.isPending}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={deleting !== null}
        title={`Delete runpack ${deleting?.Name ?? ""}?`}
        body={<p>Removes the runpack and its phase rows. There is no API to recreate it.</p>}
        confirmLabel="Delete runpack"
        destructive
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.ID)}
        onCancel={() => setDeleting(null)}
      />
    </section>
  );
}
