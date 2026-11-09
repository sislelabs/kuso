import { useSyncExternalStore } from "react";

// needsAttention mirrors the server's attentionPredicate
// (internal/db/notification_events.go): failures and warnings, fired
// alerts, security findings and kuso update notices. Applied client-side
// too because servers older than the ?attention= filter ignore it. An
// alert's all-clear keeps the alert.fired type, so it's excluded by state.
export function needsAttention(e: { type: string; severity?: string; extra?: Record<string, string> }): boolean {
  if (e.type === "alert.fired" && e.extra?.state === "resolved") return false;
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

// Per-row read state. The server only tracks a read watermark per user
// (read-all), so a row opened from the bell is remembered here until the
// next "Mark all read" moves the watermark past it. Browser-local on
// purpose: losing it only means the row shows as new again.
export interface LocalRead {
  id: number;
  attention: boolean;
}

const READ_KEY = "kuso.bell.read";
const READ_CAP = 200;
const readListeners = new Set<() => void>();
const EMPTY_READS: readonly LocalRead[] = [];
let readCache: readonly LocalRead[] | null = null;

function readLocal(): readonly LocalRead[] {
  if (readCache) return readCache;
  try {
    const raw = window.localStorage.getItem(READ_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : null;
    if (Array.isArray(parsed)) {
      readCache = parsed.flatMap((v: unknown): LocalRead[] => {
        if (!v || typeof v !== "object") return [];
        const r = v as Record<string, unknown>;
        return typeof r.id === "number" ? [{ id: r.id, attention: r.attention === true }] : [];
      });
      return readCache;
    }
  } catch {
    /* storage blocked or corrupt */
  }
  readCache = EMPTY_READS;
  return readCache;
}

function writeLocal(next: readonly LocalRead[]) {
  readCache = next.slice(-READ_CAP);
  try {
    window.localStorage.setItem(READ_KEY, JSON.stringify(readCache));
  } catch {
    /* per-tab only */
  }
  readListeners.forEach((l) => l());
}

export function markLocallyRead(e: { id: number; type: string; severity?: string }) {
  const cur = readLocal();
  if (cur.some((r) => r.id === e.id)) return;
  writeLocal([...cur, { id: e.id, attention: needsAttention(e) }]);
}

// Drop ids the server now reports read (or every id, after read-all).
export function forgetLocalReads(ids?: ReadonlySet<number>) {
  const cur = readLocal();
  const next = ids ? cur.filter((r) => !ids.has(r.id)) : EMPTY_READS;
  if (next.length !== cur.length) writeLocal(next);
}

function subscribeReads(l: () => void) {
  readListeners.add(l);
  const onStorage = (e: StorageEvent) => {
    if (e.key === READ_KEY) {
      readCache = null;
      l();
    }
  };
  window.addEventListener("storage", onStorage);
  return () => {
    readListeners.delete(l);
    window.removeEventListener("storage", onStorage);
  };
}

export function useLocalReads(): readonly LocalRead[] {
  return useSyncExternalStore(subscribeReads, readLocal, () => EMPTY_READS);
}

// Badge text: the count is capped so it stays a corner dot, not a pill.
export function badgeCount(n: number): string {
  return n > 9 ? "9+" : String(n);
}
