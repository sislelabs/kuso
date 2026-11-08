"use client";

import { Keyboard } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

export interface ShortcutEntry {
  keys: string[];
  label: string;
}

export const CANVAS_SHORTCUTS: ShortcutEntry[] = [
  { keys: ["Tab"], label: "Move between nodes" },
  { keys: ["Enter"], label: "Open the focused node" },
  { keys: ["j", "k"], label: "Next / previous service" },
  { keys: ["l"], label: "Logs of the selected service" },
  { keys: ["v"], label: "Variables of the selected service" },
  { keys: ["s"], label: "Settings of the selected service" },
  { keys: ["r"], label: "Redeploy the selected service" },
  { keys: ["?"], label: "Show this list" },
];

// announceShortcuts dispatches a cancelable `kuso:shortcuts` event so a
// global help dialog can take over (by calling preventDefault). Returns
// true when nobody claimed it and the canvas should show its own list.
export function announceShortcuts(): boolean {
  if (typeof window === "undefined") return true;
  const ev = new CustomEvent("kuso:shortcuts", {
    cancelable: true,
    detail: { scope: "canvas", shortcuts: CANVAS_SHORTCUTS },
  });
  return window.dispatchEvent(ev);
}

export function CanvasShortcuts({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (next && !announceShortcuts()) return;
        onOpenChange(next);
      }}
    >
      <PopoverTrigger
        aria-label="Keyboard shortcuts"
        className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-2.5 text-xs font-medium text-[var(--text-secondary)] shadow-[var(--shadow-sm)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] data-[popup-open]:bg-[var(--bg-tertiary)]"
      >
        <Keyboard className="h-3.5 w-3.5" aria-hidden />
        Shortcuts
        <kbd className="rounded border border-[var(--border-subtle)] px-1 font-mono text-[11px] leading-4 text-[var(--text-tertiary)]">
          ?
        </kbd>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 gap-0 p-3">
        <p className="mb-2 text-sm font-medium text-[var(--text-primary)]">Canvas shortcuts</p>
        <dl className="space-y-1.5">
          {CANVAS_SHORTCUTS.map((s) => (
            <div key={s.label} className="flex items-center justify-between gap-3 text-xs">
              <dt className="text-[var(--text-secondary)]">{s.label}</dt>
              <dd className="flex shrink-0 gap-1">
                {s.keys.map((k) => (
                  <kbd
                    key={k}
                    className="min-w-5 rounded border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-1.5 text-center font-mono text-[11px] leading-5 text-[var(--text-primary)]"
                  >
                    {k}
                  </kbd>
                ))}
              </dd>
            </div>
          ))}
        </dl>
        <p className="mt-3 text-xs text-[var(--text-tertiary)]">Right-click a node or the canvas for more actions.</p>
      </PopoverContent>
    </Popover>
  );
}
