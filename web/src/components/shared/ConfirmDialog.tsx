"use client";

import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { X, AlertTriangle } from "lucide-react";

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

  // Reset the typed text every time we open. Otherwise users
  // accidentally bypass the gate by reusing a prior session's input.
  useEffect(() => {
    if (open) {
      setText("");
      // Defer focus until the spring-in lands, otherwise the focus
      // ring jumps before the modal is visually settled.
      window.setTimeout(() => inputRef.current?.focus(), 80);
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
      if (e.key === "Enter" && allow()) onConfirm();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, text, pending]);

  const allow = () => !pending && (typeToConfirm ? text === typeToConfirm : true);

  return (
    <AnimatePresence>
      {open && (
        <motion.div
          className="fixed inset-0 z-[60] flex items-center justify-center bg-[rgba(8,8,11,0.7)] p-4"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.1 }}
          onClick={onCancel}
        >
          <motion.div
            initial={{ scale: 0.96, y: 6 }}
            animate={{ scale: 1, y: 0 }}
            exit={{ scale: 0.96, y: 6 }}
            transition={{ type: "spring", stiffness: 360, damping: 32 }}
            onClick={(e) => e.stopPropagation()}
            className={`w-full max-w-md rounded-md border bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)] ${
              destructive ? "border-red-500/40" : "border-[var(--border-subtle)]"
            }`}
          >
            <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
              <div className="flex items-center gap-2">
                {destructive && <AlertTriangle className="h-4 w-4 text-red-400" />}
                <h2 className="text-sm font-semibold tracking-tight">{title}</h2>
              </div>
              <button
                type="button"
                onClick={onCancel}
                aria-label="Cancel"
                className="inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-3 w-3" />
              </button>
            </header>

            <div className="space-y-3 px-4 py-3 text-sm text-[var(--text-secondary)]">
              <div>{body}</div>
              {typeToConfirm && (
                <div className="space-y-1">
                  <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    type{" "}
                    <span className="text-[var(--text-primary)]">{typeToConfirm}</span>{" "}
                    to confirm
                  </div>
                  <Input
                    ref={inputRef}
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    spellCheck={false}
                    className="h-8 font-mono text-[12px]"
                  />
                </div>
              )}
            </div>

            <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
              <Button variant="ghost" size="sm" onClick={onCancel} disabled={pending}>
                Cancel
              </Button>
              <Button
                variant={destructive ? "destructive" : "default"}
                size="sm"
                onClick={onConfirm}
                disabled={!allow()}
              >
                {pending ? "Working…" : confirmLabel}
              </Button>
            </footer>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
