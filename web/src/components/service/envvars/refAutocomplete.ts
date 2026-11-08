// Pure caret logic for the `${{ target.KEY }}` autocomplete in value
// inputs. The component owns rendering + key handling; this decides what
// the user is in the middle of typing and what text a pick produces.

export interface RefTrigger {
  // Index of the "$" that opened the ref.
  start: number;
  // Text between "${{" and the caret, leading whitespace dropped.
  query: string;
  // target part before the first "." (or the whole query when no dot).
  target: string;
  // key part after the dot; null while the user is still picking a target.
  key: string | null;
}

// findRefTrigger returns the open `${{ …` the caret sits in, or null when
// the caret isn't inside an unterminated ref.
export function findRefTrigger(value: string, caret: number): RefTrigger | null {
  const head = value.slice(0, caret);
  const start = head.lastIndexOf("${{");
  if (start < 0) return null;
  const inner = head.slice(start + 3);
  if (inner.includes("}}")) return null;
  const query = inner.replace(/^\s+/, "");
  // A space after a complete token means the user moved on.
  if (/\S\s/.test(query)) return null;
  const dot = query.indexOf(".");
  if (dot < 0) return { start, query, target: query, key: null };
  return { start, query, target: query.slice(0, dot), key: query.slice(dot + 1) };
}

// applyRefPick replaces the open ref with the picked completion. Picking a
// target leaves "${{ target." open so the key list follows; picking a key
// closes the ref and swallows an already-typed "}}".
export function applyRefPick(
  value: string,
  caret: number,
  trigger: RefTrigger,
  pick: { target: string; key?: string },
): { value: string; caret: number } {
  let tailStart = caret;
  if (pick.key) {
    const m = value.slice(caret).match(/^[A-Za-z0-9_.]*\s*\}\}/);
    if (m) tailStart = caret + m[0].length;
  }
  const insert = pick.key ? `\${{ ${pick.target}.${pick.key} }}` : `\${{ ${pick.target}.`;
  const next = value.slice(0, trigger.start) + insert + value.slice(tailStart);
  return { value: next, caret: trigger.start + insert.length };
}

// rankMatches keeps candidates containing the query (case-insensitive),
// prefix matches first, original order otherwise.
export function rankMatches<T>(items: readonly T[], query: string, label: (t: T) => string): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  const pre: T[] = [];
  const inner: T[] = [];
  for (const it of items) {
    const l = label(it).toLowerCase();
    if (l.startsWith(q)) pre.push(it);
    else if (l.includes(q)) inner.push(it);
  }
  return [...pre, ...inner];
}
