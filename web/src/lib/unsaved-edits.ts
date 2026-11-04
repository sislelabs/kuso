"use client";

import { useEffect } from "react";

// App-wide registry of forms holding unsaved edits. ServerVersionGuard
// reads it so the post-upgrade auto-reload never throws away a
// half-finished edit; it waits and leaves the Reload button to the user.
const dirtySources = new Set<string>();

export function setUnsavedEdits(source: string, dirty: boolean): void {
  if (dirty) dirtySources.add(source);
  else dirtySources.delete(source);
}

export function hasUnsavedEdits(): boolean {
  return dirtySources.size > 0;
}

// useUnsavedEdits registers `dirty` under `source` for the component's
// lifetime and clears it on unmount.
export function useUnsavedEdits(source: string, dirty: boolean): void {
  useEffect(() => {
    setUnsavedEdits(source, dirty);
    return () => setUnsavedEdits(source, false);
  }, [source, dirty]);
}

// editingInProgress is the fallback for forms that don't register: a
// focused text field means the user is likely mid-edit.
export function editingInProgress(): boolean {
  if (typeof document === "undefined") return false;
  const el = document.activeElement;
  if (!(el instanceof HTMLElement)) return false;
  return el.isContentEditable || el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement || el instanceof HTMLSelectElement;
}
