"use client";

import { useCallback, useRef } from "react";

// useRovingGrid turns a card grid into one tab stop. Inside the returned
// container:
//   - `[data-roving-item]` is each card's primary focus target (its link);
//   - `[data-roving-cell]` wraps one card;
//   - any focusable inside a cell other than the item is a secondary
//     control (star, menu, external links).
// Only the active card's item and secondary controls are tabbable, so Tab
// walks into the grid, through the current card, and out. Arrow keys move
// between cards (Up/Down by on-screen position, so it follows the
// responsive column count and spans several grids in one container),
// Home/End jump to the first/last card.
const FOCUSABLE = "a[href], button:not([disabled]), input, select, textarea, [tabindex]";

// Returns a callback ref for the container (it may mount after a loading
// state, so a plain ref + mount effect would miss it).
export function useRovingGrid<T extends HTMLElement>(): (node: T | null) => void {
  const active = useRef(0);
  const cleanup = useRef<(() => void) | null>(null);

  return useCallback((root: T | null) => {
    cleanup.current?.();
    cleanup.current = null;
    if (!root) return;

    const items = () => Array.from(root.querySelectorAll<HTMLElement>("[data-roving-item]"));
    const apply = () => {
      const list = items();
      if (list.length === 0) return;
      if (active.current >= list.length) active.current = list.length - 1;
      list.forEach((item, i) => {
        const on = i === active.current;
        item.tabIndex = on ? 0 : -1;
        const cell = item.closest<HTMLElement>("[data-roving-cell]");
        cell?.querySelectorAll<HTMLElement>(FOCUSABLE).forEach((el) => {
          if (el !== item && !el.hasAttribute("data-roving-item")) el.tabIndex = on ? 0 : -1;
        });
      });
    };

    const focusAt = (i: number) => {
      const list = items();
      const next = list[Math.max(0, Math.min(list.length - 1, i))];
      if (!next) return;
      active.current = list.indexOf(next);
      apply();
      next.focus();
      next.scrollIntoView({ block: "nearest" });
    };

    // The card in the next/previous visual row whose centre is closest.
    const vertical = (from: HTMLElement, dir: 1 | -1): number => {
      const list = items();
      const a = from.getBoundingClientRect();
      const ax = a.left + a.width / 2;
      let best = -1;
      let bestRowGap = Infinity;
      let bestDx = Infinity;
      list.forEach((el, i) => {
        const b = el.getBoundingClientRect();
        const gap = dir === 1 ? b.top - a.bottom : a.top - b.bottom;
        if (gap < -1) return;
        const dx = Math.abs(b.left + b.width / 2 - ax);
        if (gap < bestRowGap - 4 || (Math.abs(gap - bestRowGap) <= 4 && dx < bestDx)) {
          best = i;
          bestRowGap = gap;
          bestDx = dx;
        }
      });
      return best;
    };

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.altKey || e.ctrlKey || e.metaKey) return;
      const target = e.target as HTMLElement | null;
      const item = target?.closest<HTMLElement>("[data-roving-cell]")?.querySelector<HTMLElement>("[data-roving-item]");
      // Only from the card's own link: arrows inside a menu or an input
      // keep their native meaning.
      if (!item || target !== item) return;
      const list = items();
      const i = list.indexOf(item);
      let next = -1;
      if (e.key === "ArrowRight") next = i + 1;
      else if (e.key === "ArrowLeft") next = i - 1;
      else if (e.key === "ArrowDown") next = vertical(item, 1);
      else if (e.key === "ArrowUp") next = vertical(item, -1);
      else if (e.key === "Home") next = 0;
      else if (e.key === "End") next = list.length - 1;
      else return;
      e.preventDefault();
      if (next >= 0 && next < list.length) focusAt(next);
    };

    const onFocusIn = (e: FocusEvent) => {
      const cell = (e.target as HTMLElement | null)?.closest<HTMLElement>("[data-roving-cell]");
      const item = cell?.querySelector<HTMLElement>("[data-roving-item]");
      if (!item) return;
      const i = items().indexOf(item);
      if (i >= 0 && i !== active.current) {
        active.current = i;
        apply();
      }
    };

    apply();
    root.addEventListener("keydown", onKeyDown);
    root.addEventListener("focusin", onFocusIn);
    // Cards come and go with filters and refetches.
    const mo = new MutationObserver(apply);
    mo.observe(root, { childList: true, subtree: true });
    cleanup.current = () => {
      root.removeEventListener("keydown", onKeyDown);
      root.removeEventListener("focusin", onFocusIn);
      mo.disconnect();
    };
  }, []);
}
