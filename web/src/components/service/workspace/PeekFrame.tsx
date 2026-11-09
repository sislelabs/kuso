"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { cn } from "@/lib/utils";

// A second click this soon after opening belongs to a double-click on the
// canvas node, which opens the full page. The backdrop lets it through.
const DOUBLE_CLICK_GRACE_MS = 350;

// PeekFrame is the side-panel chrome around a workspace: dimmed backdrop
// (click to close), a panel sliding in from the right, body scroll lock.
export function PeekFrame({
  open,
  label,
  requestClose,
  wide,
  children,
}: {
  open: boolean;
  label: string;
  requestClose: () => void;
  wide: boolean;
  children: React.ReactNode;
}) {
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    if (!open) {
      setSettled(false);
      return;
    }
    const t = window.setTimeout(() => setSettled(true), DOUBLE_CLICK_GRACE_MS);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.clearTimeout(t);
      document.body.style.overflow = prevOverflow;
    };
  }, [open]);

  return (
    <AnimatePresence>
      {open && (
        <div className="fixed inset-0 z-50 flex" role="dialog" aria-modal="true" aria-label={label}>
          <motion.button
            type="button"
            aria-label="Close"
            tabIndex={-1}
            onClick={requestClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
            className={cn(
              "absolute inset-0 bg-[var(--scrim)] backdrop-blur-[2px] lg:bg-[var(--scrim-wide)] lg:backdrop-blur-none",
              !settled && "pointer-events-none",
            )}
          />
          {/* relative z-10 lifts the panel above the absolutely-positioned
              backdrop so it isn't filtered by the blur. */}
          <motion.div
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            className={cn(
              "relative z-10 ml-auto flex h-full w-full flex-col bg-[var(--bg-primary)] shadow-[var(--shadow-lg)] sm:border-l sm:border-[var(--border-subtle)]",
              // Logs and Settings need the room more than the rest: 64rem
              // from 1440px up, 48rem below. One arbitrary value because a
              // separate min-[1440px] class sorts before sm: and loses.
              wide ? "sm:max-w-[max(48rem,min(64rem,calc((100vw_-_1439px)*10000)))]" : "sm:max-w-3xl",
            )}
          >
            {children}
          </motion.div>
        </div>
      )}
    </AnimatePresence>
  );
}
