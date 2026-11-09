"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { cn } from "@/lib/utils";

// A second click this soon after opening belongs to a double-click on the
// canvas node, which opens the full page. The backdrop lets it through.
const DOUBLE_CLICK_GRACE_MS = 350;

// One width for every tab (switching tabs never makes the panel jump);
// the user can drag the left edge and the width is remembered.
const WIDTH_KEY = "kuso.peek.width";
export const PEEK_DEFAULT_WIDTH = 896;
const PEEK_MIN_WIDTH = 600;
// Always leave a strip of canvas visible to click back to.
const CANVAS_GUTTER = 96;
const KEY_STEP = 48;

function maxWidth(): number {
  return typeof window === "undefined" ? PEEK_DEFAULT_WIDTH : Math.max(PEEK_MIN_WIDTH, window.innerWidth - CANVAS_GUTTER);
}

export function clampPeekWidth(w: number, max: number): number {
  return Math.round(Math.min(Math.max(w, PEEK_MIN_WIDTH), Math.max(PEEK_MIN_WIDTH, max)));
}

function loadWidth(): number {
  try {
    const v = Number(window.localStorage.getItem(WIDTH_KEY));
    return Number.isFinite(v) && v > 0 ? v : PEEK_DEFAULT_WIDTH;
  } catch {
    return PEEK_DEFAULT_WIDTH;
  }
}

function saveWidth(w: number) {
  try {
    window.localStorage.setItem(WIDTH_KEY, String(w));
  } catch {
    // Storage blocked: the width lasts for this page view.
  }
}

// PeekFrame is the side-panel chrome around a workspace: dimmed backdrop
// (click to close), a panel sliding in from the right, body scroll lock.
export function PeekFrame({
  open,
  label,
  requestClose,
  children,
}: {
  open: boolean;
  label: string;
  requestClose: () => void;
  children: React.ReactNode;
}) {
  const [settled, setSettled] = useState(false);
  const [width, setWidth] = useState(PEEK_DEFAULT_WIDTH);
  const [dragging, setDragging] = useState(false);
  const widthRef = useRef(width);
  widthRef.current = width;

  useEffect(() => {
    setWidth(loadWidth());
  }, []);

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

  const onPointerDown = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const el = e.currentTarget;
    el.setPointerCapture(e.pointerId);
    setDragging(true);
    const move = (ev: PointerEvent) => setWidth(clampPeekWidth(window.innerWidth - ev.clientX, maxWidth()));
    const up = () => {
      setDragging(false);
      saveWidth(widthRef.current);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
  }, []);

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const max = maxWidth();
    let next: number | null = null;
    // The handle is on the left edge: Left widens, Right narrows.
    if (e.key === "ArrowLeft") next = width + KEY_STEP;
    else if (e.key === "ArrowRight") next = width - KEY_STEP;
    else if (e.key === "Home") next = PEEK_MIN_WIDTH;
    else if (e.key === "End") next = max;
    if (next === null) return;
    e.preventDefault();
    const w = clampPeekWidth(next, max);
    setWidth(w);
    saveWidth(w);
  };

  const shown = clampPeekWidth(width, maxWidth());

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
              (!settled || dragging) && "pointer-events-none",
            )}
          />
          {/* relative z-10 lifts the panel above the absolutely-positioned
              backdrop so it isn't filtered by the blur. */}
          <motion.div
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            style={{ "--peek-w": `${shown}px` } as React.CSSProperties}
            className={cn(
              "relative z-10 ml-auto flex h-full w-full flex-col bg-[var(--bg-primary)] shadow-[var(--shadow-lg)] sm:max-w-[var(--peek-w)] sm:border-l sm:border-[var(--border-subtle)]",
              dragging && "select-none",
            )}
          >
            <div
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize panel"
              aria-valuenow={shown}
              aria-valuemin={PEEK_MIN_WIDTH}
              aria-valuemax={maxWidth()}
              tabIndex={0}
              title="Drag to resize (← → with the keyboard)"
              onPointerDown={onPointerDown}
              onKeyDown={onKeyDown}
              onDoubleClick={() => {
                setWidth(PEEK_DEFAULT_WIDTH);
                saveWidth(PEEK_DEFAULT_WIDTH);
              }}
              className={cn(
                "absolute inset-y-0 -left-1 z-20 hidden w-2 cursor-col-resize touch-none sm:block",
                "after:absolute after:inset-y-0 after:left-[3px] after:w-0.5 after:transition-colors hover:after:bg-[var(--focus)] focus-visible:outline-offset-[-2px]",
                dragging && "after:bg-[var(--focus)]",
              )}
            />
            {children}
          </motion.div>
        </div>
      )}
    </AnimatePresence>
  );
}
