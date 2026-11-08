"use client";

import { useEffect, useId, useRef } from "react";
import { Lock, RotateCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { claimEscape } from "@/lib/escape-layer";
import { useModalFocus } from "@/lib/use-modal-focus";
import { cn } from "@/lib/utils";
import { diffSize, type EnvDiff, type EnvDiffItem } from "./rawEnv";

// EnvDiffDialog is the review step before an env save: what gets added,
// changed and removed (secret-looking values masked), and what the save
// does to running pods. Nothing is written until Confirm.
export function EnvDiffDialog({
  open,
  diff,
  restartNote,
  confirming,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  diff: EnvDiff;
  restartNote: string;
  confirming: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useModalFocus(open, panelRef);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      claimEscape(e);
      if (!confirming) onCancel();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [open, onCancel, confirming]);
  if (!open) return null;
  const total = diffSize(diff);
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      className="fixed inset-0 z-[150] flex items-end justify-center bg-black/60 backdrop-blur-sm sm:items-center"
      onClick={() => !confirming && onCancel()}
    >
      <div
        ref={panelRef}
        className="flex max-h-[85vh] w-full max-w-xl flex-col overflow-hidden rounded-t-2xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)] sm:m-3 sm:rounded-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="border-b border-[var(--border-subtle)] px-5 py-4">
          <h2 id={titleId} className="font-heading text-sm font-semibold tracking-tight text-[var(--text-primary)]">
            Review {total} {total === 1 ? "change" : "changes"}
          </h2>
          <p className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-[var(--text-secondary)]">
            {diff.added.length > 0 && <span className="text-[var(--success)]">{diff.added.length} added</span>}
            {diff.changed.length > 0 && <span className="text-[var(--warning)]">{diff.changed.length} changed</span>}
            {diff.removed.length > 0 && <span className="text-[var(--error)]">{diff.removed.length} removed</span>}
          </p>
        </header>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4">
          <Section title="Added" tone="add" items={diff.added} />
          <Section title="Changed" tone="change" items={diff.changed} />
          <Section title="Removed" tone="remove" items={diff.removed} />
        </div>
        <div className="flex items-start gap-2 border-t border-[var(--border-subtle)] bg-[var(--warning-subtle)] px-5 py-2.5 text-xs text-[var(--text-primary)]">
          <RotateCw className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--warning)]" />
          <span>{restartNote}</span>
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-5 py-3">
          <Button size="sm" variant="ghost" onClick={onCancel} disabled={confirming}>
            Back to editing
          </Button>
          <Button size="sm" onClick={onConfirm} disabled={confirming || total === 0}>
            {confirming ? "Saving…" : "Save and restart"}
          </Button>
        </footer>
      </div>
    </div>
  );
}

function Section({ title, tone, items }: { title: string; tone: "add" | "change" | "remove"; items: EnvDiffItem[] }) {
  if (items.length === 0) return null;
  const sign = tone === "add" ? "+" : tone === "remove" ? "−" : "~";
  const color =
    tone === "add" ? "text-[var(--success)]" : tone === "remove" ? "text-[var(--error)]" : "text-[var(--warning)]";
  return (
    <section>
      <h3 className="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">
        {title} <span className="text-[var(--text-tertiary)]">{items.length}</span>
      </h3>
      <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)]">
        {items.map((it) => (
          <li key={it.name} className="px-3 py-2">
            <div className="flex items-center gap-2">
              <span className={cn("w-3 shrink-0 font-mono text-xs", color)} aria-hidden>
                {sign}
              </span>
              <span className="min-w-0 truncate font-mono text-xs font-medium text-[var(--text-primary)]">{it.name}</span>
              {it.masked && (
                <span title="Value hidden: looks like a secret" className="text-[var(--text-tertiary)]">
                  <Lock className="h-3 w-3" />
                </span>
              )}
            </div>
            <div className="mt-1 space-y-0.5 pl-5 font-mono text-xs">
              {it.before !== undefined && tone !== "add" && (
                <p className={cn("whitespace-pre-wrap break-all", tone === "remove" ? "text-[var(--text-secondary)]" : "text-[var(--text-tertiary)] line-through")}>
                  {it.before}
                </p>
              )}
              {it.after !== undefined && <p className="whitespace-pre-wrap break-all text-[var(--text-primary)]">{it.after}</p>}
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
