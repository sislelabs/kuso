"use client";

import { MAIN_SKIP_TARGET, focusSkipTarget, useSkipTargets } from "@/lib/skip-targets";

// SkipLinks is the first thing Tab reaches on every page. Each link stays
// visually hidden until focused, then appears in the top-left corner.
export function SkipLinks() {
  const extra = useSkipTargets();
  const all = [MAIN_SKIP_TARGET, ...extra.filter((t) => t.id !== MAIN_SKIP_TARGET.id)];
  return (
    <nav aria-label="Skip links" className="contents">
      {all.map((t) => (
        <a
          key={t.id}
          href={`#${t.id}`}
          onClick={(e) => {
            if (focusSkipTarget(t.id)) e.preventDefault();
          }}
          className="sr-only z-[100] rounded-md bg-[var(--bg-elevated)] px-3 py-2 text-sm font-medium text-[var(--text-primary)] shadow-[var(--shadow-md)] focus:not-sr-only focus:absolute focus:top-2 focus:left-2"
        >
          {t.label}
        </a>
      ))}
    </nav>
  );
}
