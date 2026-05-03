"use client";

import { useEffect, useRef } from "react";
import { motion, AnimatePresence } from "motion/react";
import { cn } from "@/lib/utils";

export interface ContextMenuItem {
  id: string;
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

interface Props {
  open: boolean;
  x: number;
  y: number;
  items: ContextMenuItem[];
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
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    window.addEventListener("keydown", onKey);
    // Wait a tick so the same right-click that opened the menu doesn't
    // immediately register as a click-outside.
    const t = window.setTimeout(() => window.addEventListener("mousedown", onClick), 0);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onClick);
      window.clearTimeout(t);
    };
  }, [open, onClose]);

  // Clamp to viewport so the menu doesn't render off-screen near the
  // bottom-right corner of the canvas.
  const vw = typeof window !== "undefined" ? window.innerWidth : 1024;
  const vh = typeof window !== "undefined" ? window.innerHeight : 768;
  const w = 220;
  const h = items.length * 32 + 8;
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
          {items.map((item) => {
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
