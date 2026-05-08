"use client";

import { useEffect, useState } from "react";
import { Monitor, X } from "lucide-react";

const STORAGE_KEY = "kuso.dismissedMobileInterstitial.v1";
const SMALL_VIEWPORT_BREAKPOINT_PX = 600;

// MobileInterstitial nudges users on phones to switch to a desktop
// before the canvas / overlays melt down. Two-button modal: "I know,
// just let me in" stores a per-browser dismissal so the next visit
// doesn't pester them, and "Open on desktop" copies the URL.
//
// Why interstitial vs full layout rewrite: the canvas + service
// overlay are genuinely desktop-shaped (multi-pane forms, log
// panels, side-by-side diff). A real responsive rewrite is weeks of
// work; an interstitial is one component and tells the user the
// truth before they hit a UX wall.
//
// The few flows that DO work on phones (logs view, redeploy button,
// build status) survive a dismissal — we don't gate them. We just
// set expectations.
export function MobileInterstitial() {
  const [show, setShow] = useState(false);

  useEffect(() => {
    if (typeof window === "undefined") return;
    if (window.localStorage.getItem(STORAGE_KEY)) return;
    const isSmall = window.innerWidth < SMALL_VIEWPORT_BREAKPOINT_PX;
    if (isSmall) setShow(true);
  }, []);

  if (!show) return null;

  const dismiss = () => {
    try {
      window.localStorage.setItem(STORAGE_KEY, String(Date.now()));
    } catch {
      /* storage may be disabled — fine, we'll show again next visit */
    }
    setShow(false);
  };

  const copyUrl = async () => {
    try {
      await navigator.clipboard.writeText(window.location.href);
    } catch {
      /* not all browsers expose clipboard on http; ignore */
    }
    dismiss();
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 z-[200] flex items-end justify-center bg-black/60 backdrop-blur-sm sm:hidden"
    >
      <div className="m-3 w-full max-w-md rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-5 shadow-2xl">
        <div className="flex items-start gap-3">
          <Monitor className="mt-0.5 h-6 w-6 shrink-0 text-[var(--accent)]" />
          <div className="min-w-0 flex-1">
            <h2 className="font-heading text-base font-semibold tracking-tight">
              kuso is desktop-shaped
            </h2>
            <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
              The canvas + service overlays don&apos;t fit on a phone yet. Logs
              and redeploy work, but anything else will be cramped. We
              recommend opening this on a laptop.
            </p>
          </div>
          <button
            type="button"
            aria-label="Dismiss"
            onClick={dismiss}
            className="-mr-1 -mt-1 rounded-md p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          <button
            type="button"
            onClick={copyUrl}
            className="flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--accent)] px-3 py-2 text-xs font-semibold text-[var(--bg-primary)] hover:opacity-90"
          >
            Copy link for desktop
          </button>
          <button
            type="button"
            onClick={dismiss}
            className="flex-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
          >
            Continue anyway
          </button>
        </div>
      </div>
    </div>
  );
}
