"use client";

import { useEffect, useRef } from "react";

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

const CHORD_TIMEOUT_MS = 1200;

function isTypingTarget(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  if (el.isContentEditable) return true;
  if (el.closest(".xterm")) return true;
  const tag = el.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag === "INPUT") {
    const type = (el as HTMLInputElement).type;
    return !["checkbox", "radio", "button", "submit", "reset", "range", "color"].includes(type);
  }
  return false;
}

// useOverlayKeyboard gives a slide-over dialog its keyboard model:
//  - "g" then a letter jumps to a tab (chords map letter → action),
//  - Tab / Shift+Tab stay inside the panel,
//  - focus moves into the panel on open and back to whatever had it
//    (usually the canvas node) on close.
// Escape stays with the overlay, which already handles the unsaved-edits
// confirm.
export function useOverlayKeyboard({
  open,
  panelRef,
  chords,
}: {
  open: boolean;
  panelRef: React.RefObject<HTMLElement | null>;
  chords: Record<string, () => void>;
}) {
  const chordsRef = useRef(chords);
  useEffect(() => {
    chordsRef.current = chords;
  });

  useEffect(() => {
    if (!open) return;
    const restoreTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    // Wait a frame so the panel has mounted before we move focus into it.
    const raf = requestAnimationFrame(() => {
      const panel = panelRef.current;
      if (!panel || panel.contains(document.activeElement)) return;
      // The panel itself (tabIndex=-1) takes focus so screen readers land
      // in the dialog without a focus ring on some arbitrary control.
      panel.focus({ preventScroll: true });
    });

    let pendingG = 0;
    const onKey = (e: KeyboardEvent) => {
      const panel = panelRef.current;
      if (!panel) return;
      // Dialogs/popovers opened from the overlay portal out of it; leave
      // their keyboard handling alone.
      const target = e.target as Node | null;
      const insideOrBody = !target || target === document.body || panel.contains(target);

      if (e.key === "Tab" && insideOrBody) {
        const items = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
          (el) => el.offsetParent !== null || el === document.activeElement,
        );
        if (items.length === 0) return;
        const first = items[0];
        const last = items[items.length - 1];
        const activeEl = document.activeElement;
        if (e.shiftKey && (activeEl === first || !panel.contains(activeEl))) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && (activeEl === last || !panel.contains(activeEl))) {
          e.preventDefault();
          first.focus();
        }
        return;
      }

      if (!insideOrBody || e.metaKey || e.ctrlKey || e.altKey || isTypingTarget(e.target)) return;
      const key = e.key.toLowerCase();
      const now = Date.now();
      if (pendingG && now - pendingG < CHORD_TIMEOUT_MS) {
        pendingG = 0;
        const action = chordsRef.current[key];
        if (action) {
          e.preventDefault();
          e.stopPropagation();
          action();
        }
        return;
      }
      if (key === "g" && !e.shiftKey) {
        pendingG = now;
        // Keep the canvas/global handlers from also starting a "g" chord.
        e.stopPropagation();
      }
    };
    // Capture so the overlay's chords win over page-level shortcuts.
    window.addEventListener("keydown", onKey, true);
    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("keydown", onKey, true);
      if (restoreTo && restoreTo.isConnected) restoreTo.focus({ preventScroll: true });
    };
  }, [open, panelRef]);
}
