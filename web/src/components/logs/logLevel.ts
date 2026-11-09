// Log level detection and multi-line block folding for the log viewer.
// Pure (no React) so the rules can be table-tested. The error rules are a
// superset of the server's errorscan patterns (server-go/internal/errorscan/
// scanner.go): a line the Errors tab counts must read as an error line here
// too. The shared fixture in server-go/internal/errorscan/testdata/
// levels.json pins that agreement from both sides.

export type LogLevel = "error" | "warn" | "info" | "debug" | "none";

export const LOG_LEVELS: Exclude<LogLevel, "none">[] = ["error", "warn", "info", "debug"];

const LEVEL_ALIASES: Record<string, LogLevel> = {
  fatal: "error",
  panic: "error",
  crit: "error",
  critical: "error",
  emerg: "error",
  emergency: "error",
  alert: "error",
  err: "error",
  error: "error",
  severe: "error",
  warning: "warn",
  warn: "warn",
  wrn: "warn",
  notice: "info",
  info: "info",
  inf: "info",
  information: "info",
  log: "info",
  debug: "debug",
  dbg: "debug",
  trace: "debug",
  verbose: "debug",
};

// pino / bunyan numeric levels: 10 trace, 20 debug, 30 info, 40 warn,
// 50 error, 60 fatal.
function numericLevel(n: number): LogLevel {
  if (n >= 50) return "error";
  if (n >= 40) return "warn";
  if (n >= 30) return "info";
  return "debug";
}

// eslint-disable-next-line no-control-regex
export const ANSI_RE = /\x1b\[[0-9;?]*[A-Za-z]/g;

export function stripAnsi(s: string): string {
  return s.replace(ANSI_RE, "");
}

// A level field in JSON, logfmt or a printed object (Node's util.inspect
// writes `severity: 'ERROR',` with single quotes). Postgres JSON logs use
// error_severity.
const KV_LEVEL =
  /(?:^|[\s{,])["']?(?:level|lvl|severity|error_severity|loglevel|log\.level|levelname)["']?\s*[:=]\s*["']?([A-Za-z]+|\d{1,2})\b["']?/i;
// A 5xx response in a structured line is an error whatever level the
// request logger used (errorscan counts it too).
const STATUS_5XX = /["']?status(?:Code)?["']?\s*[:=]\s*5\d\d\b/;
// klog / glog: "E1109 12:00:00.123456 1 file.go:42] …".
const KLOG = /^([IWEF])\d{4} \d{2}:\d{2}:\d{2}/;
// Next.js prints ⨯ for errors and ⚠ for warnings.
const GLYPH = /^\s*(⨯|⚠)/;
const BRACKET_LEVEL = /^\W{0,3}(?:\S+\s+){0,3}?[[(<]?(FATAL|PANIC|CRIT(?:ICAL)?|SEVERE|ERRO?R?|WARN(?:ING)?|WRN|INFO|INF|NOTICE|DEBUG|DBG|TRACE)\b[\])>:]?/;
// Postgres server log prefixes ("2026-11-09 10:00:00.123 UTC [42] ERROR:  …").
// DETAIL/HINT/… lines belong to the message above them, so they carry no
// level of their own and fold into it.
const PG_PREFIX = /(?:^|\s)(LOG|NOTICE|INFO|WARNING|ERROR|FATAL|PANIC|DEBUG[1-5]?|DETAIL|HINT|STATEMENT|CONTEXT|LOCATION|QUERY):\s/;
const PG_LEVEL: Record<string, LogLevel> = {
  LOG: "info",
  NOTICE: "info",
  INFO: "info",
  WARNING: "warn",
  ERROR: "error",
  FATAL: "error",
  PANIC: "error",
};
// nginx-style bracketed level after a timestamp: "2026/11/09 12:00:00
// [emerg] 1#1: …". Lower-case, so the leading-token match misses it.
const BRACKETED_LEVEL = /\[(emerg|alert|crit|critical|error|err|warn|warning|notice|info|debug)\]/i;
const ERROR_SHAPES =
  /(^panic:|Traceback \(most recent call last\)|\bUncaught\b|\bunhandled(?: promise)? rejection\b|UnhandledPromiseRejection|UnhandledRejection|\bException in thread\b|^\s*at .+\(.+:\d+:\d+\)$|^[\w.$]*(?:Error|Exception)(?::|\s*$)|^\[?Error[:\]])/i;
// Error class names anywhere ("…: [TypeError: fetch failed]"), case-
// sensitive so "multierror" and "<ErrorBoundary>" stay out.
const ERROR_CLASS = /\b[A-Z][A-Za-z]*(?:Error|Exception)\b/;

// An upper-case ERROR / FATAL / PANIC word anywhere near the start
// ("worker | 12:00 ERROR …"), which the leading-token match misses.
const SHOUTED_ERROR = /\b(?:ERROR|FATAL|PANIC|SEVERE)\b/;

// detectLevel reads a level off structured fields first (JSON / logfmt /
// printed objects), then well-known prefixes (klog, Postgres, a leading
// LEVEL token, nginx brackets), then error shapes. Unknown → "none"
// rather than guessing "info" so the level filter doesn't lie.
export function detectLevel(raw: string): LogLevel {
  const line = stripAnsi(raw);
  const head400 = line.slice(0, 400);
  if (STATUS_5XX.test(head400)) return "error";
  const kv = KV_LEVEL.exec(head400);
  if (kv) {
    const v = kv[1].toLowerCase();
    if (/^\d+$/.test(v)) return numericLevel(parseInt(v, 10));
    const hit = LEVEL_ALIASES[v];
    if (hit) return hit;
  }
  const klog = KLOG.exec(line);
  if (klog) return ({ I: "info", W: "warn", E: "error", F: "error" } as const)[klog[1] as "I" | "W" | "E" | "F"];
  const glyph = GLYPH.exec(line);
  if (glyph) return glyph[1] === "⨯" ? "error" : "warn";
  const head = BRACKET_LEVEL.exec(line.slice(0, 120));
  if (head) {
    const tok = head[1].toLowerCase();
    const hit = LEVEL_ALIASES[tok] ?? (tok.startsWith("err") ? "error" : undefined);
    if (hit) return hit;
  }
  const pg = PG_PREFIX.exec(line.slice(0, 120));
  if (pg) return PG_LEVEL[pg[1]] ?? (pg[1].startsWith("DEBUG") ? "debug" : "none");
  const bracketed = BRACKETED_LEVEL.exec(line.slice(0, 80));
  if (bracketed) {
    const hit = LEVEL_ALIASES[bracketed[1].toLowerCase()];
    if (hit) return hit;
  }
  if (ERROR_SHAPES.test(line) || ERROR_CLASS.test(line) || SHOUTED_ERROR.test(line.slice(0, 200))) return "error";
  return "none";
}

const SEVERITY: Record<LogLevel, number> = { none: 0, debug: 1, info: 2, warn: 3, error: 4 };

export function moreSevere(a: LogLevel, b: LogLevel): LogLevel {
  return SEVERITY[b] > SEVERITY[a] ? b : a;
}

// A line that continues the one above it: indented (stack frames, the
// fields of a printed object), a trace marker, a closing brace, or a
// Postgres DETAIL/HINT line.
export const CONT_SHAPE =
  /^(\s|at |File "|Caused by|\.\.\. \d+ more|goroutine \d+|\}|\]|(?:DETAIL|HINT|STATEMENT|CONTEXT|LOCATION|QUERY):\s)/;

export interface FoldInput {
  pod: string;
  // Epoch ms; NaN when unknown.
  t: number;
  backlog?: boolean;
  // detectLevel of the line itself.
  level: LogLevel;
  json: boolean;
  // ANSI-stripped text.
  text: string;
}

export interface FoldResult {
  cont: boolean;
  // The level shown and filtered on: a block (head + continuations)
  // shares the most severe level found in any of its lines, so a printed
  // object whose `severity: 'ERROR'` sits on its third line counts as an
  // error from its first line on.
  level: LogLevel;
  // Index of the block's head line.
  head: number;
}

// foldBlocks ties continuation lines to their head. A continuation shares
// its head's pod and instant (a long trace flushed over several writes may
// trail by up to a second if it's trace-shaped), and either looks like a
// continuation or has no level of its own after an error/warn head.
// Replayed lines carry no time, so only the shape can tie them together.
export function foldBlocks(lines: readonly FoldInput[]): FoldResult[] {
  const out: FoldResult[] = [];
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    const prev = i > 0 ? lines[i - 1] : null;
    const prevRes = i > 0 ? out[i - 1] : null;
    let cont = false;
    if (prev && prevRes && prev.pod === l.pod && !l.json) {
      const bothBacklog = !!prev.backlog && !!l.backlog;
      const shape = CONT_SHAPE.test(l.text);
      const gap = Math.abs(prev.t - l.t);
      const sameInstant = bothBacklog || gap <= 2 || (shape && gap <= 1000);
      const blockLevel = prevRes.level;
      cont =
        sameInstant &&
        (shape || (l.level === "none" && !bothBacklog && (blockLevel === "error" || blockLevel === "warn")));
    }
    if (cont && prevRes) {
      const head = prevRes.head;
      const level = moreSevere(out[head].level, l.level);
      if (level !== out[head].level) for (let k = head; k < i; k++) out[k].level = level;
      out.push({ cont: true, level, head });
    } else {
      out.push({ cont: false, level: l.level, head: i });
    }
  }
  return out;
}
