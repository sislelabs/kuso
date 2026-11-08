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
//  - focus moves into the panel on open and, on close, back to the thing
//    that opened it: the element carrying data-focus-key={restoreKey}
//    (canvas node or list row, looked up fresh because the canvas
//    re-renders its nodes), else whatever had focus, else the canvas.
// Escape stays with the overlay, which already handles the unsaved-edits
// confirm.
export function useOverlayKeyboard({
  open,
  panelRef,
  chords,
  restoreKey,
}: {
  open: boolean;
  panelRef: React.RefObject<HTMLElement | null>;
  chords: Record<string, () => void>;
  restoreKey?: string;
}) {
  const chordsRef = useRef(chords);
  const restoreKeyRef = useRef(restoreKey);
  // Last mounted panel element, for the close-time focus check (the ref
  // itself may be empty or already pointing elsewhere by then).
  const lastPanelRef = useRef<HTMLElement | null>(null);
  useEffect(() => {
    chordsRef.current = chords;
    if (restoreKey) restoreKeyRef.current = restoreKey;
    if (panelRef.current) lastPanelRef.current = panelRef.current;
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
      const key = restoreKeyRef.current;
      const panelEl = lastPanelRef.current;
      // After the exit frame: the panel's own focus is gone by then and
      // a re-rendered canvas node is back in the DOM.
      requestAnimationFrame(() => restoreFocus(key, restoreTo, panelEl));
    };
  }, [open, panelRef]);
}

function visible(el: HTMLElement | null): el is HTMLElement {
  return !!el && el.isConnected && el.getClientRects().length > 0;
}

function restoreFocus(key: string | undefined, prev: HTMLElement | null, panel: HTMLElement | null, attempt = 0) {
  const active = document.activeElement;
  // Leave it alone if something else (the palette, a dialog) took focus.
  const stillOurs = !active || active === document.body || !active.isConnected || !!panel?.contains(active);
  if (!stillOurs) return;
  const byKey = key ? document.querySelector<HTMLElement>(`[data-focus-key="${CSS.escape(key)}"]`) : null;
  const target = [byKey, prev && prev !== document.body ? prev : null, document.getElementById("canvas-root")].find(visible);
  if (!target) return;
  target.focus({ preventScroll: true });
  // While the panel plays its exit animation the browser can refuse the
  // move; try again until it has gone (about 0.5s at most).
  if (document.activeElement !== target && attempt < 6) {
    setTimeout(() => restoreFocus(key, prev, panel, attempt + 1), 100);
  }
}
