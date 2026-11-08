"use client";

import { Keyboard } from "lucide-react";
import { Kbd } from "@/components/ui/kbd";
import { openShortcutsHelp, useRegisterShortcuts, type Shortcut } from "@/lib/shortcuts";

// Bound in ProjectCanvas's key handler; listed in the global "?" dialog.
export const CANVAS_SHORTCUTS: readonly Shortcut[] = [
  { keys: ["Tab"], description: "Move between nodes" },
  { keys: ["Enter"], description: "Open the focused node" },
  { keys: ["J", "K"], description: "Next / previous service" },
  { keys: ["L"], description: "Logs of the selected service" },
  { keys: ["V"], description: "Variables of the selected service" },
  { keys: ["S"], description: "Settings of the selected service" },
  { keys: ["R"], description: "Redeploy the selected service" },
];

// announceShortcuts dispatches a cancelable `kuso:shortcuts` event so the
// global help dialog can claim it (preventDefault). The Canvas group is
// already in the shortcut registry (`registered: true`), so a listener
// only needs to open the dialog, not add the list again. Returns true
// when nobody claimed it.
export function announceShortcuts(): boolean {
  if (typeof window === "undefined") return true;
  const ev = new CustomEvent("kuso:shortcuts", {
    cancelable: true,
    detail: { scope: "Canvas", registered: true, shortcuts: CANVAS_SHORTCUTS },
  });
  return window.dispatchEvent(ev);
}

export function useCanvasShortcuts() {
  useRegisterShortcuts("Canvas", CANVAS_SHORTCUTS);
}

// The canvas's "Shortcuts ?" button opens the global help dialog (the
// "?" key itself is handled globally).
export function CanvasShortcutsButton() {
  return (
    <button
      type="button"
      onClick={() => {
        if (announceShortcuts()) openShortcutsHelp();
      }}
      aria-label="Keyboard shortcuts"
      aria-keyshortcuts="?"
      className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-2.5 text-xs font-medium text-[var(--text-secondary)] shadow-[var(--shadow-sm)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Keyboard className="h-3.5 w-3.5" aria-hidden />
      Shortcuts
      <Kbd>?</Kbd>
    </button>
  );
}
