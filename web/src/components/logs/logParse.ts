// Pure helpers for the log viewer: ANSI SGR parsing, JSON field
// extraction and text export. Level detection lives in logLevel.ts. No
// React here so it's cheap to unit-test.

import { ANSI_RE, LOG_LEVELS, stripAnsi } from "./logLevel";
import type { LogLevel } from "./logLevel";

export { detectLevel, LOG_LEVELS, stripAnsi } from "./logLevel";
export type { LogLevel } from "./logLevel";

// The level filter's chips: the four levels plus "other" for lines with no
// detectable level, so the chip counts add up to the lines in view.
export const LOG_FILTER_LEVELS: LogLevel[] = [...LOG_LEVELS, "none"];

// parseJsonLine returns the flattened top-level fields when the line is a
// JSON object (optionally behind a short prefix), else null.
export function parseJsonLine(raw: string): Record<string, unknown> | null {
  const line = stripAnsi(raw).trim();
  const start = line.indexOf("{");
  if (start < 0 || start > 40 || !line.endsWith("}")) return null;
  try {
    const v: unknown = JSON.parse(line.slice(start));
    if (v && typeof v === "object" && !Array.isArray(v)) return v as Record<string, unknown>;
  } catch {
    return null;
  }
  return null;
}

// ---- ANSI -----------------------------------------------------------------

export interface AnsiStyle {
  fg?: string; // a class name (16-colour palette) or an rgb() string
  bg?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
}

export interface AnsiSegment {
  text: string;
  style: AnsiStyle;
}

// The 16 base colours map to Tailwind palette classes so they track the
// theme (light mode gets the darker -700 steps via the dark: variant split
// in AnsiText). Kept as names here; AnsiText maps them to classes.
export const ANSI_NAMES = [
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
] as const;
export type AnsiName = (typeof ANSI_NAMES)[number] | `bright-${(typeof ANSI_NAMES)[number]}`;

function xterm256(n: number): string {
  if (n < 8) return ANSI_NAMES[n];
  if (n < 16) return `bright-${ANSI_NAMES[n - 8]}`;
  if (n < 232) {
    const i = n - 16;
    const c = (x: number) => (x === 0 ? 0 : 55 + x * 40);
    return `rgb(${c(Math.floor(i / 36))},${c(Math.floor(i / 6) % 6)},${c(i % 6)})`;
  }
  const g = 8 + (n - 232) * 10;
  return `rgb(${g},${g},${g})`;
}

// parseAnsi splits a line into styled segments. Only SGR (…m) sequences
// carry style; any other escape (cursor moves etc.) is dropped.
export function parseAnsi(raw: string): AnsiSegment[] {
  if (!raw.includes("\x1b[")) return [{ text: raw, style: {} }];
  const out: AnsiSegment[] = [];
  let style: AnsiStyle = {};
  let last = 0;
  ANSI_RE.lastIndex = 0;
  for (let m = ANSI_RE.exec(raw); m; m = ANSI_RE.exec(raw)) {
    if (m.index > last) out.push({ text: raw.slice(last, m.index), style });
    last = m.index + m[0].length;
    const seq = m[0];
    if (!seq.endsWith("m")) continue;
    const codes = seq
      .slice(2, -1)
      .split(";")
      .map((c) => (c === "" ? 0 : parseInt(c, 10)));
    style = applySgr(style, codes);
  }
  if (last < raw.length) out.push({ text: raw.slice(last), style });
  return out.filter((s) => s.text.length > 0);
}

function applySgr(prev: AnsiStyle, codes: number[]): AnsiStyle {
  const s: AnsiStyle = { ...prev };
  for (let i = 0; i < codes.length; i++) {
    const c = codes[i];
    if (c === 0) {
      for (const k of Object.keys(s) as (keyof AnsiStyle)[]) delete s[k];
    } else if (c === 1) s.bold = true;
    else if (c === 2) s.dim = true;
    else if (c === 3) s.italic = true;
    else if (c === 4) s.underline = true;
    else if (c === 22) {
      s.bold = false;
      s.dim = false;
    } else if (c === 23) s.italic = false;
    else if (c === 24) s.underline = false;
    else if (c >= 30 && c <= 37) s.fg = ANSI_NAMES[c - 30];
    else if (c >= 90 && c <= 97) s.fg = `bright-${ANSI_NAMES[c - 90]}`;
    else if (c === 39) delete s.fg;
    else if (c >= 40 && c <= 47) s.bg = ANSI_NAMES[c - 40];
    else if (c >= 100 && c <= 107) s.bg = `bright-${ANSI_NAMES[c - 100]}`;
    else if (c === 49) delete s.bg;
    else if (c === 38 || c === 48) {
      const key = c === 38 ? "fg" : "bg";
      if (codes[i + 1] === 5 && codes[i + 2] !== undefined) {
        s[key] = xterm256(codes[i + 2]);
        i += 2;
      } else if (codes[i + 1] === 2 && codes[i + 4] !== undefined) {
        s[key] = `rgb(${codes[i + 2]},${codes[i + 3]},${codes[i + 4]})`;
        i += 4;
      }
    }
  }
  return s;
}

// ---- search highlighting --------------------------------------------------

// highlightNeedles turns a search string into the substrings to mark.
// A leading "/" means regex (live filter only); otherwise the whole
// string is one case-insensitive needle, same as the server's ILIKE.
export function buildMatcher(q: string): { test: (s: string) => boolean; re: RegExp } | null {
  const t = q.trim();
  if (!t) return null;
  if (t.startsWith("/") && t.length > 1) {
    try {
      const re = new RegExp(t.slice(1), "gi");
      const once = new RegExp(t.slice(1), "i");
      return { test: (s) => once.test(s), re };
    } catch {
      return null;
    }
  }
  const re = new RegExp(escapeRe(t), "gi");
  const lower = t.toLowerCase();
  return { test: (s) => s.toLowerCase().includes(lower), re };
}

export function escapeRe(s: string): string {
  return s.replace(/[-/\\^$*+?.()|[\]{}]/g, "\\$&");
}

// splitMatches splits text into [plain, match, plain, match, …].
export function splitMatches(text: string, re: RegExp | null): { text: string; match: boolean }[] {
  if (!re) return [{ text, match: false }];
  const out: { text: string; match: boolean }[] = [];
  let last = 0;
  const g = new RegExp(re.source, re.flags.includes("g") ? re.flags : re.flags + "g");
  for (let m = g.exec(text); m; m = g.exec(text)) {
    if (m[0].length === 0) {
      g.lastIndex++;
      continue;
    }
    if (m.index > last) out.push({ text: text.slice(last, m.index), match: false });
    out.push({ text: m[0], match: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ text: text.slice(last), match: false });
  return out.length ? out : [{ text, match: false }];
}

// ---- export ---------------------------------------------------------------

export function linesToText(
  lines: { ts: string; pod: string; line: string; service?: string }[],
  withMeta: boolean,
): string {
  return lines
    .map((l) => {
      const msg = stripAnsi(l.line);
      if (!withMeta) return msg;
      const svc = l.service ? `${l.service} ` : "";
      // A replayed line without a time keeps the column with a dash.
      return `${l.ts || "-"} ${svc}${l.pod} ${msg}`;
    })
    .join("\n");
}

export function shortPod(pod: string): string {
  const parts = pod.split("-");
  if (parts.length <= 2) return pod;
  return parts.slice(-2).join("-");
}

export function fmtClock(iso: string, withDate: boolean): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  const time = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
  if (!withDate) return time;
  const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  return `${months[d.getMonth()]} ${d.getDate()} ${time}`;
}

// levelCountText labels a level chip as lines loaded in this view
// ("5 error lines in view") so "0" can't be read as the Errors tab's
// error total, which spans 24h.
export function levelCountText(level: LogLevel, n: number): { count: string; noun: string } {
  const count = n.toLocaleString();
  switch (level) {
    case "error":
      return { count, noun: n === 1 ? "error line in view" : "error lines in view" };
    case "warn":
      return { count, noun: n === 1 ? "warning in view" : "warnings in view" };
    case "none":
      return { count, noun: n === 1 ? "other line in view" : "other lines in view" };
    default:
      return { count, noun: `${level} in view` };
  }
}

// dayChange returns the label for a date divider when `ts` falls on a
// different local day than `prevTs`, else null. Unparseable times never
// start a new day.
export function dayChange(prevTs: string | undefined, ts: string): string | null {
  if (!prevTs) return null;
  const a = new Date(prevTs);
  const b = new Date(ts);
  if (Number.isNaN(a.getTime()) || Number.isNaN(b.getTime())) return null;
  if (a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()) return null;
  return b.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", year: a.getFullYear() === b.getFullYear() ? undefined : "numeric" });
}

// sortByTime puts the whole buffer in time order: archive pages and
// live frames interleave several pods, and the replayed backlog arrives
// pod by pod. Continuation lines travel with their head; a line with no
// usable time (replayed backlog without an app timestamp) stays right
// after whatever preceded it. Ties keep arrival order.
export function sortByTime<T extends { ts: string; cont: boolean; backlog?: boolean }>(rows: T[]): T[] {
  type Group = { t: number; i: number; rows: T[] };
  const groups: Group[] = [];
  let lead: T[] = [];
  for (const r of rows) {
    const t = r.backlog ? NaN : Date.parse(r.ts);
    const last = groups[groups.length - 1];
    if (r.cont || Number.isNaN(t)) {
      if (last) last.rows.push(r);
      else lead.push(r);
      continue;
    }
    groups.push({ t, i: groups.length, rows: [r] });
  }
  if (groups.length < 2) return rows;
  let sorted = true;
  for (let k = 1; k < groups.length; k++) if (groups[k].t < groups[k - 1].t) sorted = false;
  if (sorted) return rows;
  groups.sort((a, b) => a.t - b.t || a.i - b.i);
  const out = lead;
  lead = [];
  for (const g of groups) out.push(...g.rows);
  return out;
}

// Lines the log pipeline writes about itself rather than the app's
// output: the kubelet's "unable to retrieve container logs …" (the
// server prefixes it "WARN kubelet: ") and its symlink lookups for a
// rotated-away log file.
// Multi-container pods prefix each line with "<container> | ".
const COLLECTOR_NOTE = /^(?:[\w.-]+ \| )?(?:WARN kubelet: )?(?:unable to retrieve container logs|failed to try resolving symlinks in path)/;
export function isCollectorNote(raw: string): boolean {
  return COLLECTOR_NOTE.test(stripAnsi(raw).trimStart());
}

// A leading ISO-8601 timestamp the app printed itself ("2026-11-09T07:55:29Z
// INFO …"). The row already shows a time, so the viewer strips it. ts is
// set only when the stamp names its zone; a zone-less one is ambiguous
// (container-local) and only stripped when the row has its own time.
const LEADING_ISO = /^\[?(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d{1,9})?)(Z|[+-]\d{2}:?\d{2})?\]?\s+/;
export function splitLeadingTimestamp(line: string, rowHasTime: boolean): { ts?: string; rest: string } | null {
  const m = LEADING_ISO.exec(line);
  if (!m) return null;
  const rest = line.slice(m[0].length);
  if (!rest.trim()) return null;
  const base = m[1].replace(" ", "T").replace(",", ".");
  if (m[2]) {
    const zone = m[2] === "Z" ? "Z" : m[2].length === 5 ? `${m[2].slice(0, 3)}:${m[2].slice(3)}` : m[2];
    const ms = Date.parse(base + zone);
    if (Number.isNaN(ms)) return null;
    return { ts: new Date(ms).toISOString(), rest };
  }
  if (!rowHasTime || Number.isNaN(Date.parse(base + "Z"))) return null;
  return { rest };
}

// focusBlock finds the occurrence another tab pointed at: the head line
// nearest `focus.ts` (within a minute) whose text contains the needle,
// plus its continuation lines. Empty when nothing matches, so a wrong
// line is never marked.
export function focusBlock<T extends { key: string; ts: string; cont: boolean; line: string }>(
  rows: readonly T[],
  focus: { ts: string; needle: string } | null | undefined,
): Set<string> {
  const out = new Set<string>();
  const needle = focus?.needle.trim().toLowerCase();
  const at = Date.parse(focus?.ts ?? "");
  if (!needle || Number.isNaN(at)) return out;
  let best = -1;
  let bestGap = 60_000;
  rows.forEach((r, i) => {
    if (r.cont) return;
    const gap = Math.abs(Date.parse(r.ts) - at);
    if (gap <= bestGap && stripAnsi(r.line).toLowerCase().includes(needle)) {
      best = i;
      bestGap = gap;
    }
  });
  if (best < 0) return out;
  out.add(rows[best].key);
  for (let i = best + 1; i < rows.length && rows[i].cont; i++) out.add(rows[i].key);
  return out;
}
