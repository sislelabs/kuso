import { useSyncExternalStore } from "react";

// needsAttention mirrors the server's attentionPredicate
// (internal/db/notification_events.go): failures and warnings, fired
// alerts, security findings and kuso update notices. Applied client-side
// too because servers older than the ?attention= filter ignore it.
export function needsAttention(e: { type: string; severity?: string }): boolean {
  return (
    e.severity === "warn" ||
    e.severity === "error" ||
    e.type === "alert.fired" ||
    e.type === "update.available" ||
    e.type.startsWith("security.")
  );
}

export type BellView = "attention" | "all";

export interface BellPrefs {
  view: BellView;
  // When true the unread badge counts every event, successes included.
  countAll: boolean;
}

const KEY = "kuso.bell.prefs";
const DEFAULTS: BellPrefs = { view: "attention", countAll: false };
const listeners = new Set<() => void>();
let cache: BellPrefs | null = null;

function read(): BellPrefs {
  if (cache) return cache;
  try {
    const raw = window.localStorage.getItem(KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : null;
    if (parsed && typeof parsed === "object") {
      const p = parsed as Record<string, unknown>;
      cache = {
        view: p.view === "all" ? "all" : "attention",
        countAll: p.countAll === true,
      };
      return cache;
    }
  } catch {
    /* storage blocked or corrupt: defaults */
  }
  cache = DEFAULTS;
  return cache;
}

export function setBellPrefs(patch: Partial<BellPrefs>) {
  cache = { ...read(), ...patch };
  try {
    window.localStorage.setItem(KEY, JSON.stringify(cache));
  } catch {
    /* per-tab only */
  }
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  const onStorage = (e: StorageEvent) => {
    if (e.key === KEY) {
      cache = null;
      l();
    }
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(l);
    window.removeEventListener("storage", onStorage);
  };
}

export function useBellPrefs(): BellPrefs {
  return useSyncExternalStore(subscribe, read, () => DEFAULTS);
}

// collapseUpdates keeps only the newest "update available" event (the
// feed is newest-first) and drops it entirely once the server says it's
// current, so one release never shows as several stale rows.
export function collapseUpdates<T extends { type: string }>(events: T[], needsUpdate: boolean | undefined): T[] {
  let seen = false;
  return events.filter((e) => {
    if (e.type !== "update.available") return true;
    if (needsUpdate === false || seen) return false;
    seen = true;
    return true;
  });
}
