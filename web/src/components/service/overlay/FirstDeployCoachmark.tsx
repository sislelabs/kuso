"use client";

import { Sparkles, X } from "lucide-react";
import { useEffect, useState } from "react";

// LocalStorage key that gates the coachmark. Once a user has seen +
// dismissed it, every subsequent overlay open skips the tour. Per-
// browser-profile, not per-user — kuso has no per-user UI state
// table yet and the cost of accidentally re-showing the tour on a
// different machine is roughly zero.
const TOUR_KEY = "kuso_tour_seen_first_deploy";

interface Props {
  // shouldShow is computed by ServiceOverlay from the build list: true
  // when the most recent build succeeded within the last 60s AND the
  // tour hasn't been seen yet (localStorage). We accept it as a prop
  // rather than re-deriving inside so the parent doesn't have to leak
  // its build query into this component.
  shouldShow: boolean;
}

// FirstDeployCoachmark — single in-overlay card that lights up on the
// user's very first successful deploy. Points at the 4 features a
// regular dev coming from Vercel/Render expects to find (env vars,
// logs, ref picker, rollback) so they don't have to discover each
// one by accident. Dismissed by clicking X or "Got it"; localStorage
// remembers the dismissal across sessions.
//
// Not a portal/coachmark library — those add weight and brittleness
// for one screen. An inline card inside the overlay does the job:
// the user is already looking at the overlay; we just need to point
// at what matters.
export function FirstDeployCoachmark({ shouldShow }: Props) {
  // Hydration-safe localStorage read: defer the check to a useEffect
  // so SSR + first paint match and we don't flash the card before
  // hiding it.
  const [seen, setSeen] = useState<boolean>(true);
  useEffect(() => {
    if (typeof window === "undefined") return;
    setSeen(window.localStorage.getItem(TOUR_KEY) === "1");
  }, []);
  if (seen || !shouldShow) return null;
  const dismiss = () => {
    setSeen(true);
    try {
      window.localStorage.setItem(TOUR_KEY, "1");
    } catch {
      // localStorage can throw under privacy mode + a cluster of
      // mobile Safari quirks. Treat it as a transient — the user
      // will see the tour again next deploy, no worse than no-op.
    }
  };
  return (
    <div className="m-5 mb-0 rounded-lg border border-[var(--accent)]/30 bg-[var(--accent)]/5 p-4 text-sm">
      <div className="flex items-start gap-3">
        <Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-[var(--accent)]" />
        <div className="min-w-0 flex-1">
          <div className="font-medium">Your first deploy went green.</div>
          <p className="mt-1 text-[var(--text-secondary)]">
            A few things worth knowing now you&apos;re live:
          </p>
          <ol className="mt-2 list-decimal space-y-1 pl-4 text-[var(--text-secondary)]">
            <li>
              <span className="font-medium text-[var(--text-primary)]">Variables</span>{" "}
              — set env vars. Type{" "}
              <span className="font-mono text-[var(--accent)]">${`{{`}</span> to
              link a managed addon (Postgres, Redis) or a sibling service.
            </li>
            <li>
              <span className="font-medium text-[var(--text-primary)]">Logs</span> —
              full-text search across the last 14 days, scoped to env.
            </li>
            <li>
              <span className="font-medium text-[var(--text-primary)]">Deployments</span>{" "}
              — every prior successful build has a{" "}
              <span className="font-mono">rollback</span> button. One click to
              promote any image back into production.
            </li>
            <li>
              <span className="font-medium text-[var(--text-primary)]">Settings → Scale</span>{" "}
              — autoscaling, sleep-when-idle, memory + CPU requests.
            </li>
          </ol>
          <button
            type="button"
            onClick={dismiss}
            className="mt-3 inline-flex h-7 items-center rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-[12px] font-medium text-[var(--btn-primary-fg)] transition-colors hover:bg-[var(--btn-primary-bg-hover)]"
          >
            Got it
          </button>
        </div>
        <button
          type="button"
          aria-label="Dismiss tour"
          onClick={dismiss}
          className="text-[var(--text-tertiary)] transition-colors hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
}
