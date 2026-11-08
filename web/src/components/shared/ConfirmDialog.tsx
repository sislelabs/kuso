"use client";

import { useEffect, useId, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { X, AlertTriangle, AlertCircle } from "lucide-react";
import { cn } from "@/lib/utils";
import { claimEscape } from "@/lib/escape-layer";

interface Props {
  open: boolean;
  title: string;
  body: React.ReactNode;
  confirmLabel?: string;
  // typeToConfirm gates the confirm button until the user types this
  // exact string. Required for destructive ops that take data with
  // them (delete a service with a PVC, drop a project, etc).
  typeToConfirm?: string;
  destructive?: boolean;
  pending?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

// confirmDialogKeyAction maps a window keydown to what the dialog does
// with it. Enter is deliberately absent: confirming goes through the
// form's submit (Enter in the typed-name input, or activating the
// confirm button), so Enter while Cancel/X has focus can't confirm.
export function confirmDialogKeyAction(key: string): "cancel" | null {
  return key === "Escape" ? "cancel" : null;
}

// ConfirmDialog is the typed-name confirmation modal we use across
// every destructive action — service delete, project delete, addon
// delete, restore-from-backup, etc. Single component so all our
// destructive flows behave identically: animated, ESC closes,
// click-outside cancels, gated by a typed-name when configured.
export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel = "Confirm",
  typeToConfirm,
  destructive = true,
  pending = false,
  onConfirm,
  onCancel,
}: Props) {
  const [text, setText] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  // panelRef scopes the focus trap; restoreRef remembers who had focus
  // before we opened so we can hand it back on close (a11y: focus must
  // not vanish to <body> when a modal dismisses).
  const panelRef = useRef<HTMLDivElement>(null);
  const restoreRef = useRef<HTMLElement | null>(null);
  // Stable id so aria-labelledby on the dialog can point at the title.
  const titleId = useId();

  // Reset the typed text every time we open. Otherwise users
  // accidentally bypass the gate by reusing a prior session's input.
  useEffect(() => {
    if (open) {
      setText("");
      // Remember the trigger so we can restore focus on close.
      restoreRef.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      // Defer focus until the spring-in lands, otherwise the focus
      // ring jumps before the modal is visually settled. When there's
      // a typed-name gate, land on that input; otherwise focus the
      // first focusable in the panel so keyboard users start inside
      // the trap rather than on <body>.
      window.setTimeout(() => {
        if (inputRef.current) {
          inputRef.current.focus();
          return;
        }
        const first = panelRef.current?.querySelector<HTMLElement>(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
        );
        first?.focus();
      }, 80);
    }
  }, [open]);

  // Restore focus to the opener when the dialog closes/unmounts.
  useEffect(() => {
    if (open) return;
    const el = restoreRef.current;
    restoreRef.current = null;
    // Only restore if focus is still loose (on <body>) — don't yank it
    // away if the caller already moved focus somewhere intentional.
    if (el && (document.activeElement === document.body || document.activeElement === null)) {
      el.focus();
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (confirmDialogKeyAction(e.key) === "cancel") {
        claimEscape(e);
        onCancel();
        return;
      }
      // Focus trap: keep Tab / Shift+Tab cycling inside the panel so
      // keyboard focus can't escape to page content behind the modal.
      if (e.key === "Tab") {
        const panel = panelRef.current;
        if (!panel) return;
        const focusables = Array.from(
          panel.querySelectorAll<HTMLElement>(
            'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
          ),
        ).filter((el) => el.offsetParent !== null || el === document.activeElement);
        if (focusables.length === 0) return;
        const firstEl = focusables[0];
        const lastEl = focusables[focusables.length - 1];
        const active = document.activeElement as HTMLElement | null;
        if (e.shiftKey) {
          if (active === firstEl || !panel.contains(active)) {
            e.preventDefault();
            lastEl.focus();
          }
        } else if (active === lastEl || !panel.contains(active)) {
          e.preventDefault();
          firstEl.focus();
        }
      }
    };
    // Capture phase so this runs before an enclosing overlay's window
    // listener, which then sees the Escape as claimed.
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [open, onCancel]);

  const allow = () => !pending && (typeToConfirm ? text === typeToConfirm : true);

  return (
    <AnimatePresence>
      {open && (
        <motion.div
          className="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px] dark:bg-black/60"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.1 }}
          onClick={onCancel}
        >
          <motion.div
            ref={panelRef}
            role="dialog"
            aria-modal="true"
            aria-labelledby={titleId}
            aria-describedby={`${titleId}-body`}
            initial={{ scale: 0.96, y: 6 }}
            animate={{ scale: 1, y: 0 }}
            exit={{ scale: 0.96, y: 6 }}
            transition={{ type: "spring", stiffness: 360, damping: 32 }}
            onClick={(e) => e.stopPropagation()}
            className="flex max-h-[calc(100dvh-2rem)] w-full max-w-md flex-col overflow-hidden rounded-xl border border-[var(--border-default)] bg-[var(--popover)] shadow-[var(--shadow-lg)]"
          >
            <form
              className="flex min-h-0 flex-1 flex-col"
              onSubmit={(e) => {
                e.preventDefault();
                if (allow()) onConfirm();
              }}
            >
            <header className="flex shrink-0 items-start justify-between gap-3 px-5 pt-5 pb-1">
              <div className="flex items-center gap-2.5">
                {/* Non-destructive confirms all touch running workloads
                    (rollback, redeploy, restart, revert), so they share one
                    caution treatment; destructive ones stay red. */}
                <span
                  aria-hidden
                  className={cn(
                    "flex size-7 shrink-0 items-center justify-center rounded-md",
                    destructive ? "bg-[var(--danger-subtle)]" : "bg-[var(--warning-subtle)]",
                  )}
                >
                  {destructive ? (
                    <AlertTriangle className="size-4 text-[var(--danger)]" />
                  ) : (
                    <AlertCircle className="size-4 text-[var(--warning)]" />
                  )}
                </span>
                <h2 id={titleId} className="text-base font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
              </div>
              <button
                type="button"
                onClick={onCancel}
                aria-label="Cancel"
                className="-mt-1 -mr-1 inline-flex size-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="size-4" />
              </button>
            </header>

            <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 pt-2 pb-5 text-sm text-[var(--text-secondary)]">
              <div id={`${titleId}-body`}>{body}</div>
              {typeToConfirm && (
                <div className="space-y-1.5">
                  <label htmlFor={`${titleId}-confirm`} className="block text-[13px] font-medium text-[var(--text-secondary)]">
                    Type{" "}
                    <span className="font-mono font-medium text-[var(--text-primary)]">{typeToConfirm}</span>{" "}
                    to confirm
                  </label>
                  <Input
                    ref={inputRef}
                    aria-describedby={`${titleId}-body`}
                    id={`${titleId}-confirm`}
                    autoComplete="off"
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    spellCheck={false}
                    className="h-9 font-mono text-[13px]"
                  />
                </div>
              )}
            </div>

            <footer className="flex shrink-0 items-center justify-end gap-2 border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-5 py-3">
              <Button type="button" variant="secondary" size="sm" onClick={onCancel} disabled={pending}>
                Cancel
              </Button>
              <Button
                type="submit"
                variant={destructive ? "destructive" : "primary"}
                size="sm"
                disabled={!allow()}
                loading={pending}
              >
                {confirmLabel}
              </Button>
            </footer>
            </form>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
