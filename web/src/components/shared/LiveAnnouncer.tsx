"use client";

import { useEffect, useRef, useSyncExternalStore } from "react";

// One polite live region for the whole app. Status changes that happen
// while you're looking elsewhere (a rollout going live, a build moving
// to deploy, a service starting to fail) are queued here instead of each
// component owning a region that may be unmounted or hidden.
let message = "";
let seq = 0;
const listeners = new Set<() => void>();

export function announce(text: string): void {
  if (!text) return;
  seq++;
  // Repeating the same text wouldn't re-trigger the region; a trailing
  // zero-width space every other time makes it a fresh change.
  message = seq % 2 ? text : `${text}\u200b`;
  listeners.forEach((fn) => fn());
}

export function currentAnnouncement(): string {
  return message;
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function LiveAnnouncer() {
  const text = useSyncExternalStore(
    subscribe,
    currentAnnouncement,
    () => "",
  );
  return (
    <div role="status" aria-live="polite" aria-atomic="true" className="sr-only">
      {text}
    </div>
  );
}

// useAnnounceChange announces `format(value)` whenever `value` changes
// after the first render (the initial state is already on screen).
export function useAnnounceChange(value: string | undefined, format: (v: string) => string, enabled = true): void {
  const prev = useRef<string | undefined>(undefined);
  const fmt = useRef(format);
  fmt.current = format;
  useEffect(() => {
    if (!enabled || !value) return;
    if (prev.current !== undefined && prev.current !== value) announce(fmt.current(value));
    prev.current = value;
  }, [value, enabled]);
}
