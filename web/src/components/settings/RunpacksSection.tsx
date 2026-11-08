"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
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
import { SettingsSection } from "./SettingsSection";

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
    onError: (e) => toastError(e, "Delete failed"),
    onSettled: () => setDeleting(null),
  });

  const rows = packs.data ?? [];

  return (
    <SettingsSection
      title="Runpacks"
      description="Fetch, build and run image triples stored in the control-plane database. Read-only apart from delete."
      flush
    >
      {packs.isPending ? (
        <Skeleton className="m-4 h-12" />
      ) : packs.isError ? (
        <QueryErrorState what="runpacks" error={packs.error} onRetry={() => void packs.refetch()} className="m-4" />
      ) : rows.length === 0 ? (
        <p className="px-4 py-4 text-sm text-[var(--text-secondary)] sm:px-5">No runpacks on this instance.</p>
      ) : (
        <ul>
          {rows.map((r) => (
            <li
              key={r.ID}
              className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-4 py-3 last:border-b-0 sm:px-5"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm text-[var(--text-primary)]">{r.Name}</span>
                  {r.Language && (
                    <span className="rounded-sm bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]">
                      {r.Language}
                    </span>
                  )}
                </div>
                <dl className="mt-1 grid gap-x-3 gap-y-0.5 text-xs sm:grid-cols-[auto_1fr]">
                  {([["Fetch", r.Fetch], ["Build", r.Build], ["Run", r.Run]] as const).map(([k, ph]) => (
                    <div key={k} className="contents">
                      <dt className="text-[var(--text-secondary)]">{k}</dt>
                      <dd className="min-w-0 truncate font-mono text-[var(--text-primary)]">{phaseImage(ph)}</dd>
                    </div>
                  ))}
                </dl>
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
        typeToConfirm={deleting?.Name}
        destructive
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.ID)}
        onCancel={() => setDeleting(null)}
      />
    </SettingsSection>
  );
}
