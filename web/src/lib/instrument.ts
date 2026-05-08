// Lightweight UX instrumentation. Tracks click + dialog-open events
// in a per-tab ring buffer so we can answer "which buttons get used?"
// + "where do users rage-click?" without shipping a third-party
// telemetry SDK or beaconing data off the box.
//
// Design constraints:
// - Zero network calls. Events live in memory + sessionStorage; an
//   admin can dump them via the dev console (`window.__kuso_events()`)
//   or the Settings → Diagnostics page.
// - Bounded memory. Ring buffer caps at 500 events; oldest fall off
//   silently — no allocation pressure on a long-lived dashboard tab.
// - Stable across re-renders. The buffer is module-level state; React
//   components import `track()` and call it directly.
// - Cheap. Recording a click is one push + one slice — measured at
//   <0.05ms in dev. Nothing rides this on the render path.
//
// Privacy: we record event NAMES (button labels, panel ids) but never
// values (no input contents, no service names, no project secrets).
// Rage-click detection sums clicks per name in 2s windows; if 3+ hits
// land on the same name in that window the event is flagged so the
// dump shows it as a rage cluster.

const MAX_EVENTS = 500;
const RAGE_WINDOW_MS = 2_000;
const RAGE_THRESHOLD = 3;

export interface InstrumentEvent {
  name: string;
  kind: "click" | "open" | "submit" | "navigate";
  ts: number;
  rage?: boolean;
}

const buffer: InstrumentEvent[] = [];
const recentByName = new Map<string, number[]>(); // name → [timestamps]

function pruneRecent(name: string, now: number) {
  const arr = recentByName.get(name);
  if (!arr) return;
  const fresh = arr.filter((t) => now - t < RAGE_WINDOW_MS);
  if (fresh.length === 0) {
    recentByName.delete(name);
  } else {
    recentByName.set(name, fresh);
  }
}

export function track(name: string, kind: InstrumentEvent["kind"] = "click") {
  if (typeof window === "undefined") return;
  const now = Date.now();
  pruneRecent(name, now);
  const existing = recentByName.get(name) ?? [];
  existing.push(now);
  recentByName.set(name, existing);
  const rage = kind === "click" && existing.length >= RAGE_THRESHOLD;
  buffer.push({ name, kind, ts: now, rage });
  if (buffer.length > MAX_EVENTS) {
    buffer.splice(0, buffer.length - MAX_EVENTS);
  }
}

export function getEvents(): InstrumentEvent[] {
  return buffer.slice();
}

export function getRageNames(): string[] {
  const names = new Set<string>();
  for (const e of buffer) if (e.rage) names.add(e.name);
  return [...names];
}

export function clearEvents() {
  buffer.length = 0;
  recentByName.clear();
}

if (typeof window !== "undefined") {
  // Expose a dev-console hook so an admin can grab the buffer
  // without a separate UI. Read-only — clear is a separate verb.
  (window as unknown as { __kuso_events?: () => InstrumentEvent[] }).__kuso_events =
    () => getEvents();
  (window as unknown as { __kuso_rage?: () => string[] }).__kuso_rage =
    () => getRageNames();
}
