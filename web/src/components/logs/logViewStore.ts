import type { LogLevel } from "./logParse";

// Per-viewer UI state that must survive the overlay unmounting its tab
// panels (switching Logs → Metrics → Logs used to reset the query, range
// and scroll). Module-level, keyed by scope; lives for the page session.

export type LogRange = { kind: "live" } | { kind: "preset"; preset: string } | { kind: "abs"; from: string; to: string };

export interface LogViewState {
  q: string; // the committed query (archive) / live filter
  range: LogRange;
  env: string;
  levels: LogLevel[]; // empty = all
  pod: string; // "" = all
  wrap: boolean;
  // Scroll position when the reader had scrolled away from the tail.
  scrollTop: number | null;
  // An occurrence another tab sent the reader to (Errors → View in logs):
  // its block is scrolled to, marked and kept visible through the level
  // filter. Cleared when the reader changes the query or range.
  focus?: LogFocus | null;
}

export interface LogFocus {
  ts: string;
  // Text of the occurrence's first line, highlighted in the view.
  needle: string;
}

const states = new Map<string, LogViewState>();
const listeners = new Map<string, Set<() => void>>();

export function logViewKey(project: string, service: string | undefined, env: string | undefined): string {
  return `${project}/${service ?? "*"}/${env ?? ""}`;
}

export function getLogView(key: string, init: () => LogViewState): LogViewState {
  let s = states.get(key);
  if (!s) {
    s = init();
    states.set(key, s);
  }
  return s;
}

export function setLogView(key: string, patch: Partial<LogViewState>, init?: () => LogViewState): void {
  const prev = states.get(key) ?? init?.();
  if (!prev) return;
  states.set(key, { ...prev, ...patch });
  listeners.get(key)?.forEach((fn) => fn());
}

export function subscribeLogView(key: string, fn: () => void): () => void {
  let set = listeners.get(key);
  if (!set) {
    set = new Set();
    listeners.set(key, set);
  }
  set.add(fn);
  return () => set.delete(fn);
}

// openLogsAt is how other tabs (Errors → "View in logs") hand the logs
// view a query + time window before switching tabs. Applies to every
// env of the service already open, and is queued for one not mounted yet.
export function openLogsAt(project: string, service: string, patch: Partial<LogViewState>): void {
  const prefix = `${project}/${service}/`;
  for (const key of states.keys()) {
    if (key.startsWith(prefix)) setLogView(key, { ...patch, scrollTop: null });
  }
  pending.set(`${project}/${service}`, { ...patch, scrollTop: null });
}

// A patch for a viewer that hasn't mounted yet is applied on first init.
const pending = new Map<string, Partial<LogViewState>>();

export function takePendingLogView(project: string, service: string | undefined): Partial<LogViewState> | undefined {
  if (!service) return undefined;
  const k = `${project}/${service}`;
  const p = pending.get(k);
  pending.delete(k);
  return p;
}

// ---- recent queries (per browser, best-effort) ------------------------------

const RECENT_KEY = "kuso.logs.recentQueries";

export function loadRecentQueries(): string[] {
  try {
    const raw = window.localStorage.getItem(RECENT_KEY);
    const v: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string").slice(0, 8) : [];
  } catch {
    return [];
  }
}

export function rememberQuery(q: string): string[] {
  const t = q.trim();
  const next = t ? [t, ...loadRecentQueries().filter((x) => x !== t)].slice(0, 8) : loadRecentQueries();
  try {
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(next));
  } catch {
    // storage blocked — recent queries just won't persist
  }
  return next;
}
