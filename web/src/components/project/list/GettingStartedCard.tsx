"use client";

import Link from "next/link";
import { ArrowRight, Compass, X } from "lucide-react";

// Shown on small instances so the onboarding wizard stays reachable
// after "skip to dashboard". Dismissal is per-browser.
export function GettingStartedCard({ onDismiss }: { onDismiss: () => void }) {
  return (
    <section
      aria-labelledby="getting-started-title"
      className="relative mb-6 flex flex-col gap-3 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 pr-12 sm:flex-row sm:items-center sm:gap-4"
    >
      <span className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--accent-subtle)] text-[var(--accent)]">
        <Compass className="h-4.5 w-4.5" aria-hidden />
      </span>
      <div className="min-w-0 flex-1">
        <h2 id="getting-started-title" className="text-sm font-semibold text-[var(--text-primary)]">
          Getting started
        </h2>
        <p className="mt-0.5 text-[13px] text-[var(--text-secondary)]">
          Connect GitHub, pick a repo and ship your first service in three steps. Or start from a ready-made app.
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Link
          href="/welcome"
          className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] outline-none hover:bg-[var(--btn-primary-bg-hover)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
        >
          Open the guide
          <ArrowRight className="h-3.5 w-3.5" aria-hidden />
        </Link>
        <Link
          href="/marketplace"
          className="inline-flex h-8 items-center rounded-md border border-[var(--border-subtle)] px-3 text-xs font-medium text-[var(--text-primary)] outline-none hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
        >
          Browse apps
        </Link>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss getting started"
        className="absolute right-2 top-2 inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] outline-none hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
      >
        <X className="h-4 w-4" />
      </button>
    </section>
  );
}
