"use client";

import type { Revision } from "@/features/revisions";
import { formatValue, revisionFieldChanges } from "./revisionDiff";

// RevisionDiff lists what one config revision changed, field by field:
// old → new. Mounted by RevisionRow when a "Settings changed" row is
// expanded. `history` is the loaded revision list for the same resource;
// the "old" side comes from the newest older revision that set the field.
export function RevisionDiff({ revision, history }: { revision: Revision; history: readonly Revision[] }) {
  const changes = revisionFieldChanges(revision, history);
  if (changes.length === 0) {
    return <p className="px-3 py-2 text-xs text-[var(--text-tertiary)]">This change didn&apos;t record which fields it set.</p>;
  }
  return (
    <dl className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)] gap-x-3 gap-y-1.5 px-3 py-2 text-xs">
      {changes.map((c) => (
        <div key={c.field} className="contents">
          <dt className="truncate font-mono text-[var(--text-secondary)]" title={c.field}>
            {c.field}
          </dt>
          <dd className="min-w-0 break-words">
            <span
              className={c.before === undefined ? "italic text-[var(--text-tertiary)]" : "font-mono text-[var(--text-tertiary)] line-through decoration-[var(--text-tertiary)]/50"}
              title={c.before === undefined ? "No earlier change in this history set it" : undefined}
            >
              {c.before === undefined ? "earlier value" : formatValue(c.before)}
            </span>
            <span aria-label="changed to" className="px-1.5 text-[var(--text-tertiary)]">
              →
            </span>
            <span className="font-mono text-[var(--text-primary)]">{formatValue(c.after)}</span>
          </dd>
        </div>
      ))}
    </dl>
  );
}
