"use client";

import { useEffect, useSyncExternalStore } from "react";

// Skip links: the shell always offers "Skip to content" (#main); a page
// can register more landmarks (the canvas, a log viewer) with
// useSkipTarget. Targets render in registration order after the main one.

export interface SkipTarget {
  id: string;
  label: string;
}

export const MAIN_SKIP_TARGET: SkipTarget = { id: "main", label: "Skip to content" };

let targets: SkipTarget[] = [];
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

export function registerSkipTarget(t: SkipTarget): () => void {
  targets = [...targets.filter((x) => x.id !== t.id), t];
  emit();
  return () => {
    targets = targets.filter((x) => x.id !== t.id);
    emit();
  };
}

// useSkipTarget registers "label" → the element with id `id` while the
// calling component is mounted. The element should be focusable itself
// (tabIndex 0 or -1); if it isn't, the link gives it tabindex=-1.
export function useSkipTarget(id: string, label: string) {
  useEffect(() => registerSkipTarget({ id, label }), [id, label]);
}

export function getSkipTargets(): SkipTarget[] {
  return targets;
}

const EMPTY: SkipTarget[] = [];

export function useSkipTargets(): SkipTarget[] {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    getSkipTargets,
    () => EMPTY,
  );
}

// focusSkipTarget moves focus (not just scroll) to the target, so the next
// Tab continues from there. Returns false when the element isn't mounted.
export function focusSkipTarget(id: string): boolean {
  const el = document.getElementById(id);
  if (!el) return false;
  if (!el.hasAttribute("tabindex") && el.tabIndex < 0) el.setAttribute("tabindex", "-1");
  el.focus({ preventScroll: false });
  el.scrollIntoView({ block: "nearest" });
  return true;
}
