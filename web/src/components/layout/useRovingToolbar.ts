"use client";

import { useCallback, useRef } from "react";

const FOCUSABLE = "a[href], button:not([disabled]), [role='button']:not([aria-disabled='true'])";

// useRovingToolbar makes a row of controls one tab stop (WAI-ARIA toolbar
// pattern): Tab enters on the last-used control, Left/Right/Home/End move
// within the row. Hidden controls (display:none at this breakpoint) are
// skipped. Returns a callback ref for the role="toolbar" element.
export function useRovingToolbar<T extends HTMLElement>(): (node: T | null) => void {
  const active = useRef(0);
  const cleanup = useRef<(() => void) | null>(null);

  return useCallback((root: T | null) => {
    cleanup.current?.();
    cleanup.current = null;
    if (!root) return;

    const items = () =>
      Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => el.getClientRects().length > 0);
    const apply = () => {
      const list = items();
      if (list.length === 0) return;
      if (active.current >= list.length) active.current = list.length - 1;
      list.forEach((el, i) => {
        const want = i === active.current ? 0 : -1;
        if (el.tabIndex !== want) el.tabIndex = want;
      });
    };

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.altKey || e.ctrlKey || e.metaKey) return;
      const list = items();
      const i = list.indexOf(document.activeElement as HTMLElement);
      if (i < 0) return;
      let next = i;
      if (e.key === "ArrowRight") next = (i + 1) % list.length;
      else if (e.key === "ArrowLeft") next = (i - 1 + list.length) % list.length;
      else if (e.key === "Home") next = 0;
      else if (e.key === "End") next = list.length - 1;
      else return;
      e.preventDefault();
      active.current = next;
      apply();
      list[next]?.focus();
    };
    const onFocusIn = (e: FocusEvent) => {
      const i = items().indexOf(e.target as HTMLElement);
      if (i >= 0 && i !== active.current) {
        active.current = i;
        apply();
      }
    };

    apply();
    // Controls mount late (popovers, auth-gated items) and breakpoints
    // hide some; re-stamp tabindex whenever the row changes.
    const mo = new MutationObserver(apply);
    mo.observe(root, { childList: true, subtree: true });
    window.addEventListener("resize", apply);
    root.addEventListener("keydown", onKeyDown);
    root.addEventListener("focusin", onFocusIn);
    cleanup.current = () => {
      mo.disconnect();
      window.removeEventListener("resize", apply);
      root.removeEventListener("keydown", onKeyDown);
      root.removeEventListener("focusin", onFocusIn);
    };
  }, []);
}
