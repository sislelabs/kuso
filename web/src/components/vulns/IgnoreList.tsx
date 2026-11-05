"use client";

import { useState } from "react";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useRemoveVulnIgnore, useVulnIgnores, type VulnIgnore, type VulnOwner } from "@/features/vulns";

export function IgnoreList({ owner, canEdit }: { owner: VulnOwner; canEdit: boolean }) {
  const ignores = useVulnIgnores(owner);
  const remove = useRemoveVulnIgnore(owner);
  const [pending, setPending] = useState<VulnIgnore | null>(null);

  const confirmRemove = async () => {
    if (!pending) return;
    try {
      await remove.mutateAsync(pending.id);
      toast.success(`${pending.vulnId} is no longer ignored`);
    } catch {
      // Global mutation toast reports the server message.
    } finally {
      setPending(null);
    }
  };

  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold">Ignored vulnerabilities</h3>
      {ignores.isError ? (
        <QueryErrorState what="ignores" error={ignores.error} onRetry={() => void ignores.refetch()} />
      ) : !ignores.data ? (
        <p className="text-xs text-[var(--text-tertiary)]">Loading…</p>
      ) : ignores.data.length === 0 ? (
        <p className="text-xs text-[var(--text-secondary)]">
          None. Use &ldquo;Ignore…&rdquo; on a finding to accept a risk with a recorded reason.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)]">
          {ignores.data.map((ig) => (
            <li key={ig.id} className={cn("flex items-start gap-3 px-3 py-2", ig.expired && "opacity-60")}>
              <div className="min-w-0 flex-1 space-y-0.5">
                <p className="font-mono text-xs text-[var(--text-primary)]">
                  {ig.vulnId}
                  {ig.expired && <span className="ml-2 text-[10px] text-[var(--text-tertiary)]">expired</span>}
                </p>
                <p className="break-words text-xs text-[var(--text-secondary)]">{ig.reason}</p>
                <p className="text-[10px] text-[var(--text-tertiary)]">
                  by {ig.createdBy} · {relativeTime(ig.createdAt)}
                  {ig.expiresAt && (
                    <span title={ig.expiresAt}>
                      {" "}
                      · {ig.expired ? "expired" : "expires"} {relativeTime(ig.expiresAt)}
                    </span>
                  )}
                </p>
              </div>
              {canEdit && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Stop ignoring ${ig.vulnId}`}
                  onClick={() => setPending(ig)}
                >
                  <Trash2 />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={!!pending}
        title={`Stop ignoring ${pending?.vulnId ?? ""}?`}
        body="The finding will count again toward the deploy gate and alerts."
        confirmLabel="Remove"
        pending={remove.isPending}
        onConfirm={() => void confirmRemove()}
        onCancel={() => setPending(null)}
      />
    </section>
  );
}
