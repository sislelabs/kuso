"use client";

import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { AlertCircle } from "lucide-react";

// DiffEntry is a single row in the "you're about to change" list.
// Callers compose the list — this component doesn't know what
// envVars vs port vs domains mean, only how to render the diff.
export interface DiffEntry {
  field: string;          // e.g. "envVars / DRIFT_TEST"
  before?: string;        // missing => addition
  after?: string;         // missing => removal
}

interface Props {
  open: boolean;
  title?: string;
  description?: string;
  entries: DiffEntry[];
  onCancel: () => void;
  onConfirm: () => void;
  confirmLabel?: string;
  confirming?: boolean;
}

// DiffConfirmDialog shows the user exactly what's about to be applied
// before a save runs. Used on the variables editor + service settings
// + any other "change spec → kube reconciles → user surprised" path.
//
// Why an explicit modal vs trust-the-toast: a single Save click can
// silently change ten env vars or rename a domain that triggers an
// LE certificate fetch (and a ratelimit hit). The toast-after-the-
// fact tells you it saved, not what changed. Surfacing the diff at
// commit time turns "what did I just do?" into "I confirmed each
// of these."
export function DiffConfirmDialog({
  open,
  title = "Confirm changes",
  description = "Review what's about to change.",
  entries,
  onCancel,
  onConfirm,
  confirmLabel = "Apply",
  confirming = false,
}: Props) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onCancel]);
  if (!open) return null;
  return (
    <div
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 z-[150] flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={onCancel}
    >
      <div
        className="m-3 max-h-[80vh] w-full max-w-xl overflow-hidden rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-start gap-3 border-b border-[var(--border-subtle)] px-5 py-4">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--accent)]" />
          <div className="min-w-0 flex-1">
            <h2 className="font-heading text-sm font-semibold tracking-tight">{title}</h2>
            <p className="mt-0.5 text-[12px] text-[var(--text-secondary)]">{description}</p>
          </div>
        </header>
        <div className="max-h-[50vh] overflow-y-auto px-5 py-3">
          {entries.length === 0 ? (
            <p className="py-4 text-center text-[12px] text-[var(--text-tertiary)]">
              No effective changes detected.
            </p>
          ) : (
            <ul className="space-y-2">
              {entries.map((d, i) => (
                <li
                  key={i}
                  className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2"
                >
                  <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    {d.field}
                  </div>
                  <div className="mt-1 grid grid-cols-[1fr_auto_1fr] items-center gap-2 font-mono text-[11px]">
                    <span
                      className={
                        d.before
                          ? "truncate text-red-300/80 line-through decoration-red-500/40"
                          : "italic text-[var(--text-tertiary)]"
                      }
                    >
                      {d.before || "(unset)"}
                    </span>
                    <span className="text-[var(--text-tertiary)]">→</span>
                    <span
                      className={
                        d.after
                          ? "truncate text-emerald-300"
                          : "italic text-[var(--text-tertiary)]"
                      }
                    >
                      {d.after || "(removed)"}
                    </span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-5 py-3">
          <Button size="sm" variant="ghost" onClick={onCancel} disabled={confirming}>
            Cancel
          </Button>
          <Button size="sm" onClick={onConfirm} disabled={confirming || entries.length === 0}>
            {confirming ? "Applying…" : confirmLabel}
          </Button>
        </footer>
      </div>
    </div>
  );
}
