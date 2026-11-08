// Pure helpers for the log viewer: level detection, ANSI SGR parsing,
// JSON field extraction and text export. No React here so it's cheap to
// unit-test.

export type LogLevel = "error" | "warn" | "info" | "debug" | "none";

export const LOG_LEVELS: Exclude<LogLevel, "none">[] = ["error", "warn", "info", "debug"];

const LEVEL_ALIASES: Record<string, LogLevel> = {
  fatal: "error",
  panic: "error",
  crit: "error",
  critical: "error",
  emerg: "error",
  alert: "error",
  err: "error",
  error: "error",
  warning: "warn",
  warn: "warn",
  wrn: "warn",
  notice: "info",
  info: "info",
  inf: "info",
  information: "info",
  debug: "debug",
  dbg: "debug",
  trace: "debug",
  verbose: "debug",
};

// pino / bunyan numeric levels.
function numericLevel(n: number): LogLevel {
  if (n >= 50) return "error";
  if (n >= 40) return "warn";
  if (n >= 30) return "info";
  return "debug";
}

// eslint-disable-next-line no-control-regex
const ANSI_RE = /\x1b\[[0-9;?]*[A-Za-z]/g;

export function stripAnsi(s: string): string {
  return s.replace(ANSI_RE, "");
}

const KV_LEVEL = /(?:^|[\s{,])"?(?:level|lvl|severity|loglevel)"?\s*[:=]\s*"?([A-Za-z]+|\d{2})"?/i;
const BRACKET_LEVEL = /^\W{0,3}(?:\S+\s+){0,3}?[[(<]?(FATAL|PANIC|CRIT(?:ICAL)?|ERRO?R?|WARN(?:ING)?|WRN|INFO|INF|NOTICE|DEBUG|DBG|TRACE)\b[\])>:]?/;
const ERROR_SHAPES = /(^panic:|^Traceback \(most recent call last\)|\bUncaught\b|\bunhandled(?: promise)? rejection\b|^\s*at .+\(.+:\d+:\d+\)$|^\w*(?:Error|Exception)(?::|\s*$)|^\[?Error[:\]])/i;

// detectLevel reads a level off structured fields first (JSON / logfmt),
// then a leading LEVEL token, then well-known error shapes. Unknown → "none"
// rather than guessing "info" so the level filter doesn't lie.
export function detectLevel(raw: string): LogLevel {
  const line = stripAnsi(raw);
  const kv = KV_LEVEL.exec(line.slice(0, 400));
  if (kv) {
    const v = kv[1].toLowerCase();
    if (/^\d+$/.test(v)) return numericLevel(parseInt(v, 10));
    const hit = LEVEL_ALIASES[v];
    if (hit) return hit;
  }
  const head = BRACKET_LEVEL.exec(line.slice(0, 120));
  if (head) {
    const tok = head[1].toLowerCase();
    const hit = LEVEL_ALIASES[tok] ?? (tok.startsWith("err") ? "error" : undefined);
    if (hit) return hit;
  }
  if (ERROR_SHAPES.test(line)) return "error";
  return "none";
}

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

// levelCountText labels a level chip as lines ("5 error lines") so the
// count can't be read as the Errors tab's error total.
export function levelCountText(level: Exclude<LogLevel, "none">, n: number): { count: string; noun: string } {
  const count = n.toLocaleString();
  switch (level) {
    case "error":
      return { count, noun: n === 1 ? "error line" : "error lines" };
    case "warn":
      return { count, noun: n === 1 ? "warning" : "warnings" };
    default:
      return { count, noun: level };
  }
}
