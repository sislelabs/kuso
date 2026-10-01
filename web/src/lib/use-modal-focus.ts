import { useEffect, useRef, type RefObject } from "react";

const FOCUSABLE =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// useModalFocus moves focus into the panel when `open` flips on, keeps
// Tab / Shift+Tab inside it, and hands focus back to the opener on close.
export function useModalFocus(open: boolean, panelRef: RefObject<HTMLElement | null>) {
  const restoreRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!open) return;
    restoreRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    panelRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusables = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (focusables.length === 0) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      const active = document.activeElement;
      if (e.shiftKey ? active === first || !panel.contains(active) : active === last || !panel.contains(active)) {
        e.preventDefault();
        (e.shiftKey ? last : first).focus();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      const el = restoreRef.current;
      restoreRef.current = null;
      // Only restore if focus fell back to <body>; don't steal it from
      // wherever the caller moved it on close.
      if (el && (document.activeElement === document.body || document.activeElement === null)) el.focus();
    };
  }, [open, panelRef]);
}
