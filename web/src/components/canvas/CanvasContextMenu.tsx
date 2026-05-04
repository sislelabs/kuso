"use client";

import { useEffect, useRef } from "react";
import { motion, AnimatePresence } from "motion/react";
import { cn } from "@/lib/utils";

// ContextMenuEntry covers four kinds of rows: an action button (the
// default), a non-clickable section header, a separator line, and a
// header with a small subtitle (used for the right-click target's
// name/kind). The discriminator is `kind` defaulting to "action" so
// existing callers don't need to change.
export type ContextMenuEntry =
  | ContextMenuItem
  | ContextMenuSeparator
  | ContextMenuHeader;

export interface ContextMenuItem {
  id: string;
  kind?: "action";
  label: string;
  icon?: React.ComponentType<{ className?: string }>;
  destructive?: boolean;
  disabled?: boolean;
  shortcut?: string;
  // Optional so a placeholder ("coming soon" disabled item) doesn't
  // need to ship a no-op handler. Disabled items short-circuit the
  // click before invoking this anyway.
  onSelect?: () => void;
}
export interface ContextMenuSeparator {
  id: string;
  kind: "separator";
}
export interface ContextMenuHeader {
  id: string;
  kind: "header";
  label: string;
  subtitle?: string;
  icon?: React.ComponentType<{ className?: string }>;
}

interface Props {
  open: boolean;
  x: number;
  y: number;
  items: ContextMenuEntry[];
  onClose: () => void;
}

// CanvasContextMenu is a positioned popover for right-click on the
// React Flow canvas. We don't use the existing DropdownMenu primitive
// because it expects a click trigger; the right-click flow needs an
// imperative open at a specific viewport coordinate. A bare absolutely-
// positioned card with click-outside + ESC + Tab-trap is enough.
export function CanvasContextMenu({ open, x, y, items, onClose }: Props) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    // Close on any pointer down outside the menu. We listen in the
    // capture phase on the document so React Flow's pan/zoom (which
    // stops propagation on its own pane mousedown) doesn't swallow
    // the event before we see it. pointerdown also fires for touch +
    // pen + mouse so the menu dismisses on any input modality.
    const onPointer = (e: PointerEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    window.addEventListener("keydown", onKey);
    // Wait a tick so the same right-click that opened the menu doesn't
    // immediately register as a click-outside.
    const t = window.setTimeout(
      () => document.addEventListener("pointerdown", onPointer, true),
      0
    );
    return () => {
      window.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPointer, true);
      window.clearTimeout(t);
    };
  }, [open, onClose]);

  // Clamp to viewport so the menu doesn't render off-screen near the
  // bottom-right corner of the canvas. Width grows for richer menus;
  // height estimate accounts for separators (8px) + headers (44px)
  // + actions (32px) so the clamp is roughly right.
  const vw = typeof window !== "undefined" ? window.innerWidth : 1024;
  const vh = typeof window !== "undefined" ? window.innerHeight : 768;
  const w = 240;
  const h = items.reduce((acc, it) => {
    const kind = (it as ContextMenuSeparator).kind;
    if (kind === "separator") return acc + 9;
    if (kind === "header") return acc + 44;
    return acc + 32;
  }, 8);
  const left = Math.min(x, vw - w - 8);
  const top = Math.min(y, vh - h - 8);

  return (
    <AnimatePresence>
      {open && (
        <motion.div
          ref={ref}
          initial={{ opacity: 0, scale: 0.96, y: -2 }}
          animate={{ opacity: 1, scale: 1, y: 0 }}
          exit={{ opacity: 0, scale: 0.96, y: -2 }}
          transition={{ duration: 0.1, ease: "easeOut" }}
          style={{ left, top, width: w }}
          className="fixed z-[60] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] py-1 shadow-[var(--shadow-lg)]"
          role="menu"
        >
          {items.map((entry) => {
            const kind = (entry as ContextMenuSeparator).kind ?? "action";
            if (kind === "separator") {
              return (
                <div
                  key={entry.id}
                  role="separator"
                  className="my-1 h-px bg-[var(--border-subtle)]"
                />
              );
            }
            if (kind === "header") {
              const h = entry as ContextMenuHeader;
              const Icon = h.icon;
              return (
                <div
                  key={entry.id}
                  className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-2.5 py-1.5"
                >
                  {Icon && <Icon className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />}
                  <div className="min-w-0">
                    <p className="truncate text-[12px] font-medium">{h.label}</p>
                    {h.subtitle && (
                      <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
                        {h.subtitle}
                      </p>
                    )}
                  </div>
                </div>
              );
            }
            const item = entry as ContextMenuItem;
            const Icon = item.icon;
            return (
              <button
                key={item.id}
                type="button"
                role="menuitem"
                disabled={item.disabled}
                onClick={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  if (item.disabled || !item.onSelect) return;
                  item.onSelect();
                  onClose();
                }}
                className={cn(
                  "flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-sm transition-colors",
                  item.disabled
                    ? "cursor-not-allowed text-[var(--text-tertiary)]/60"
                    : item.destructive
                      ? "text-red-400 hover:bg-red-500/10"
                      : "text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]"
                )}
              >
                {Icon ? (
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                ) : (
                  <span className="h-3.5 w-3.5" aria-hidden />
                )}
                <span className="flex-1 truncate">{item.label}</span>
                {item.shortcut && (
                  <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                    {item.shortcut}
                  </span>
                )}
              </button>
            );
          })}
        </motion.div>
      )}
    </AnimatePresence>
  );
}
