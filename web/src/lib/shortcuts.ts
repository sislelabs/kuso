"use client";

import { useEffect, useSyncExternalStore } from "react";

// Shortcut registry behind the "?" help dialog. Pages list their own
// shortcuts here so the dialog always matches what's actually bound:
//
//   useRegisterShortcuts("Canvas", [
//     { keys: ["N"], description: "Add a service" },
//     { keys: ["G", "L"], sequence: true, description: "Open logs" },
//   ]);
//
// Registering only documents a shortcut; the page still binds the key.
// openShortcutsHelp() opens the dialog from anywhere (e.g. a "?" button).

export type Shortcut = {
  keys: readonly string[];
  description: string;
  /** Keys pressed one after another rather than together. */
  sequence?: boolean;
};
export type ShortcutGroup = { id: number; title: string; items: readonly Shortcut[] };

let nextId = 1;
let groups: ShortcutGroup[] = [];
let helpOpen = false;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

export function registerShortcuts(title: string, items: readonly Shortcut[]): () => void {
  const id = nextId++;
  groups = [...groups, { id, title, items }];
  emit();
  return () => {
    groups = groups.filter((g) => g.id !== id);
    emit();
  };
}

export function useRegisterShortcuts(title: string, items: readonly Shortcut[]) {
  // Keyed on content, so an inline array literal doesn't re-register every render.
  const key = JSON.stringify(items);
  useEffect(() => registerShortcuts(title, JSON.parse(key) as Shortcut[]), [title, key]);
}

export function getShortcutGroups(): readonly ShortcutGroup[] {
  return groups;
}

export function useShortcutGroups(): ShortcutGroup[] {
  return useSyncExternalStore(subscribe, () => groups, () => groups);
}

export function openShortcutsHelp() {
  helpOpen = true;
  emit();
}
export function setShortcutsHelpOpen(open: boolean) {
  helpOpen = open;
  emit();
}
export function useShortcutsHelpOpen(): boolean {
  return useSyncExternalStore(subscribe, () => helpOpen, () => false);
}

/** True when a keystroke belongs to a text field, not to app shortcuts. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (typeof HTMLElement === "undefined" || !(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag === "INPUT") {
    const type = (target as HTMLInputElement).type;
    return !["checkbox", "radio", "button", "submit", "reset", "range", "color", "file"].includes(type);
  }
  // xterm's hidden textarea is caught above; CodeMirror-style editors set role=textbox.
  return target.getAttribute("role") === "textbox";
}

/** Focus the page's primary search box: the first visible [data-primary-search]. */
export function focusPrimarySearch(): boolean {
  const els = Array.from(document.querySelectorAll<HTMLElement>("[data-primary-search]"));
  const el = els.find((e) => e.offsetParent !== null || e.getClientRects().length > 0);
  if (!el) return false;
  el.focus();
  if (el instanceof HTMLInputElement) el.select();
  return true;
}

export const isMac = () =>
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
