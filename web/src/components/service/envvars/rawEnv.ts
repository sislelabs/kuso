// Strict dotenv parsing + the Raw-mode ↔ Row reconciliation for the env
// editor. Unlike dotenvToRows (the forgiving paste helper), every line
// here either parses or produces an error with its line number: a raw
// save that silently dropped a mistyped line would read as "deleted".
//
// Pure: no React, no DOM.

import {
  ENV_NAME_RE,
  ENV_MASK_SENTINEL,
  keptOutOfBulk,
  rid,
  type Row,
} from "@/components/service/envVarTransforms";
import { isSensitiveVar } from "@/components/service/envvars/sensitivity";

export interface RawEntry {
  name: string;
  value: string;
  // 1-based line the entry starts on.
  line: number;
}

export interface RawIssue {
  line: number;
  message: string;
}

export interface RawParseResult {
  entries: RawEntry[];
  errors: RawIssue[];
}

const DOUBLE_ESCAPES: Record<string, string> = {
  n: "\n",
  r: "\r",
  t: "\t",
  '"': '"',
  "\\": "\\",
  $: "$",
};

// Reads a quoted value starting right after the opening quote at
// lines[li][col]. Double quotes honour backslash escapes; single quotes
// are literal. Both may span lines (multi-line PEM keys, JSON blobs).
function readQuoted(
  lines: string[],
  li: number,
  col: number,
  quote: '"' | "'",
): { value: string; endLine: number; rest: string } | null {
  let out = "";
  let i = li;
  let c = col;
  while (i < lines.length) {
    const line = lines[i];
    while (c < line.length) {
      const ch = line[c];
      if (quote === '"' && ch === "\\" && c + 1 < line.length) {
        const nx = line[c + 1];
        out += DOUBLE_ESCAPES[nx] ?? "\\" + nx;
        c += 2;
        continue;
      }
      if (ch === quote) return { value: out, endLine: i, rest: line.slice(c + 1) };
      out += ch;
      c++;
    }
    out += "\n";
    i++;
    c = 0;
  }
  return null;
}

export function parseRawEnv(text: string): RawParseResult {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const entries: RawEntry[] = [];
  const errors: RawIssue[] = [];
  const firstSeen = new Map<string, number>();
  for (let li = 0; li < lines.length; li++) {
    const lineNo = li + 1;
    const trimmed = lines[li].trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const body = trimmed.replace(/^export\s+/, "");
    const eq = body.indexOf("=");
    if (eq < 0) {
      errors.push({ line: lineNo, message: `expected KEY=value, got "${clip(trimmed, 40)}"` });
      continue;
    }
    const name = body.slice(0, eq).trim();
    if (!name) {
      errors.push({ line: lineNo, message: "missing variable name before =" });
      continue;
    }
    if (!ENV_NAME_RE.test(name)) {
      errors.push({
        line: lineNo,
        message: `"${clip(name, 40)}" is not a valid name (letters, digits and _ only; can't start with a digit)`,
      });
      continue;
    }
    const after = body.slice(eq + 1).replace(/^[ \t]+/, "");
    let value: string;
    if (after.startsWith('"') || after.startsWith("'")) {
      const quote = after[0] as '"' | "'";
      // Locate the opening quote in the untrimmed line: trimming would
      // eat trailing spaces that belong to a multi-line value.
      const raw = lines[li];
      let col = raw.indexOf("=") + 1;
      while (raw[col] === " " || raw[col] === "\t") col++;
      col++;
      const q = readQuoted(lines, li, col, quote);
      if (!q) {
        errors.push({ line: lineNo, message: `${name}: unterminated ${quote === '"' ? "double" : "single"} quote` });
        break;
      }
      const tail = q.rest.trim();
      if (tail && !tail.startsWith("#")) {
        errors.push({ line: q.endLine + 1, message: `${name}: unexpected text after closing quote` });
      }
      value = q.value;
      li = q.endLine;
    } else {
      // Unquoted: an inline comment needs whitespace before the # so URL
      // fragments (https://x/#/a) survive.
      value = after.replace(/\s+#.*$/, "").trim();
    }
    const prev = firstSeen.get(name);
    if (prev !== undefined) {
      errors.push({ line: lineNo, message: `${name} is already set on line ${prev}` });
      continue;
    }
    firstSeen.set(name, lineNo);
    entries.push({ name, value, line: lineNo });
  }
  return { entries, errors };
}

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

// Stands in for a secret-looking value while secrets are hidden. Leaving
// it in place keeps the stored value; replacing it sets a new one.
export const HIDDEN_MARKER = "<hidden>";

// Marker emitted for read-only rows. A line that uncomments it would
// write the marker text as a literal value, so the reconciler rejects it.
export const READONLY_MARKER = "<read-only>";

// Quote only when the unquoted form wouldn't parse back identically, so
// refs like ${{ db.DATABASE_URL }} stay readable.
function quoteValue(v: string): string {
  const needs = v !== v.trim() || /[\n\r\t]/.test(v) || /\s#/.test(v) || /^["']/.test(v);
  if (!needs) return v;
  const escaped = v
    .replace(/\\/g, "\\\\")
    .replace(/"/g, '\\"')
    .replace(/\n/g, "\\n")
    .replace(/\r/g, "\\r")
    .replace(/\t/g, "\\t");
  return `"${escaped}"`;
}

function readonlyReason(r: Row): string {
  if (r.addon) return `from addon ${r.addon}`;
  if (r.fromSecret) return "mounted secret reference";
  return "secret value not readable";
}

// rowsToRaw serialises the editor rows for Raw mode. Rows the text can't
// round-trip (opaque secret refs, addon-injected keys, unrevealed secrets)
// become comment lines: visible, ignored on parse, and kept on save.
export function rowsToRaw(rows: Row[], opts: { hideSecrets?: boolean } = {}): string {
  const editable: string[] = [];
  const readonly: string[] = [];
  const sorted = [...rows]
    .filter((r) => r.name.trim())
    .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  for (const r of sorted) {
    if (isReadonlyInRaw(r)) {
      readonly.push(`# ${r.name}=${READONLY_MARKER}  (${readonlyReason(r)})`);
      continue;
    }
    if (opts.hideSecrets && !r.value.includes("${{") && isSensitiveVar(r.name, r.value)) {
      editable.push(`${r.name}=${HIDDEN_MARKER}`);
      continue;
    }
    editable.push(`${r.name}=${quoteValue(r.value)}`);
  }
  const out = [...editable];
  if (readonly.length) {
    if (out.length) out.push("");
    out.push("# Read-only. Kept on save; manage these in Table view.", ...readonly);
  }
  return out.join("\n") + (out.length ? "\n" : "");
}

// Managed secrets ARE editable in raw mode once their value is revealed;
// keptOutOfBulk (the old paste dialog's rule) is stricter than needed here
// because reconcileRaw keeps each row's origin flags by name.
export function isReadonlyInRaw(r: Row): boolean {
  if (r.managed && !r.fromSecret && r.value !== "" && r.value !== ENV_MASK_SENTINEL) return false;
  return keptOutOfBulk(r) || r.value === ENV_MASK_SENTINEL;
}

export interface ReconcileResult {
  rows: Row[];
  errors: RawIssue[];
}

// reconcileRaw applies parsed raw text onto the current rows, keyed by
// name. Existing rows keep their id and origin flags so the save path
// (rowUnchanged, managed-secret delete, addon override) behaves exactly as
// in Table mode. Read-only rows survive even when their comment line is
// deleted; an active line with the same name is an explicit override.
export function reconcileRaw(text: string, current: Row[]): ReconcileResult {
  const { entries, errors } = parseRawEnv(text);
  const known = new Set(current.map((r) => r.name.trim()));
  for (const e of entries) {
    if (e.value === HIDDEN_MARKER && !known.has(e.name)) {
      errors.push({ line: e.line, message: `${e.name}: ${HIDDEN_MARKER} is a placeholder. Type the value` });
    }
    if (e.value.includes(READONLY_MARKER)) {
      errors.push({
        line: e.line,
        message: `${e.name} is read-only here. Re-comment the line or set a real value`,
      });
    }
  }
  if (errors.length) return { rows: current, errors: errors.sort((a, b) => a.line - b.line) };
  const byName = new Map<string, Row>();
  for (const r of current) if (r.name.trim()) byName.set(r.name.trim(), r);
  const out: Row[] = [];
  const seen = new Set<string>();
  for (const e of entries) {
    seen.add(e.name);
    const prev = byName.get(e.name);
    if (!prev) {
      out.push({ id: rid(), name: e.name, value: e.value, fromSecret: false, secretBacked: false, visible: true });
      continue;
    }
    if (e.value === HIDDEN_MARKER || (!isReadonlyInRaw(prev) && prev.value === e.value)) {
      out.push(prev);
      continue;
    }
    out.push({ ...prev, value: e.value, fromSecret: false, origValueFrom: undefined, resolved: undefined });
  }
  for (const r of current) {
    const n = r.name.trim();
    if (n && !seen.has(n) && isReadonlyInRaw(r)) out.push(r);
  }
  return { rows: out, errors: [] };
}

// ── Diff ─────────────────────────────────────────────────────────────

export interface EnvDiffItem {
  name: string;
  before?: string;
  after?: string;
  // True when the labels are masked (secret-looking key/value).
  masked: boolean;
}

export interface EnvDiff {
  added: EnvDiffItem[];
  changed: EnvDiffItem[];
  removed: EnvDiffItem[];
}

// Short non-reversible fingerprint so a rotated secret still reads as a
// change without its plaintext reaching the DOM. FNV-1a; a UI comparison,
// not a security boundary.
export function maskDigest(v: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < v.length; i++) {
    h ^= v.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0").slice(0, 6);
}

export function diffLabel(r: Row): { text: string; masked: boolean } {
  if (r.fromSecret) return { text: "secret reference", masked: true };
  const v = r.value ?? "";
  if (v.includes("${{")) return { text: clip(v, 80), masked: false };
  if (isSensitiveVar(r.name, v)) {
    return { text: v ? `•••••••• ${maskDigest(v)}` : "••••••••", masked: true };
  }
  if (v === "") return { text: '""', masked: false };
  return { text: clip(v, 80), masked: false };
}

// buildEnvDiff turns the save plan (what will actually be written and
// deleted) into the added/changed/removed view, labelling values from the
// rows so masking follows the same heuristic as the table.
export function buildEnvDiff(
  plan: { valueWrites: { name: string; value: string }[]; deletes: string[] },
  baseline: Row[],
  rows: Row[],
): EnvDiff {
  const before = new Map<string, Row>();
  for (const b of baseline) if (b.name.trim()) before.set(b.name.trim(), b);
  const after = new Map<string, Row>();
  for (const r of rows) if (r.name.trim()) after.set(r.name.trim(), r);
  const diff: EnvDiff = { added: [], changed: [], removed: [] };
  for (const w of plan.valueWrites) {
    const a = after.get(w.name) ?? { id: "", name: w.name, value: w.value, fromSecret: false, secretBacked: false, visible: false };
    const al = diffLabel({ ...a, value: w.value });
    const b = before.get(w.name);
    if (!b) {
      diff.added.push({ name: w.name, after: al.text, masked: al.masked });
      continue;
    }
    const bl = diffLabel(b);
    diff.changed.push({ name: w.name, before: bl.text, after: al.text, masked: al.masked || bl.masked });
  }
  for (const n of plan.deletes) {
    const b = before.get(n);
    const bl = b ? diffLabel(b) : { text: "", masked: false };
    diff.removed.push({ name: n, before: bl.text, masked: bl.masked });
  }
  const byName = (x: EnvDiffItem, y: EnvDiffItem) => x.name.localeCompare(y.name);
  diff.added.sort(byName);
  diff.changed.sort(byName);
  diff.removed.sort(byName);
  return diff;
}

export function diffSize(d: EnvDiff): number {
  return d.added.length + d.changed.length + d.removed.length;
}
