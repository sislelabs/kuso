// cleanMentionRules drops mention rules that match what the server would
// do anyway, so the stored config stays small. The server resolves a rule
// per event as: exact event key, then "*", then the event's default
// (notify.mentionFor). So a per-event rule is only redundant when it
// equals the "*" rule if one exists, else the catalogue default. Without
// the catalogue every explicit rule is kept rather than risk dropping an
// opt-out.
export function cleanMentionRules(
  mentions: Record<string, string>,
  catalogue: { type: string; defaultMention: string }[] | undefined,
): Record<string, string> {
  const norm = (v: string) => (v === "none" ? "" : v);
  const wildcard = mentions["*"];
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(mentions)) {
    if (!v) continue; // "" = use default
    const known = catalogue?.find((t) => t.type === k);
    if (known) {
      const fallback = wildcard ? norm(wildcard) : known.defaultMention;
      if (norm(v) === fallback) continue;
    }
    out[k] = v;
  }
  return out;
}
