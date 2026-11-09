"use client";

import { useEffect, useState, useSyncExternalStore } from "react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useCanOnProject, Perms } from "@/features/auth";
import { useQueries } from "@tanstack/react-query";
import { discardProject, hasUnpersistedStaged, useStagedProject } from "@/features/staged";
import { getService } from "@/features/services/api";
import { serviceQueryKey } from "@/features/services/hooks";
import { reviewItemCount } from "./plan";
import { cn } from "@/lib/utils";
import { StagedReviewDialog } from "./StagedReviewDialog";
import { useAnnounceChange } from "@/components/shared/LiveAnnouncer";

// Page shells mount a fixed bar; the settings and variables panels mount
// a sticky one at the end of their content. The sticky one wins while
// it's mounted: it sits in the panel's own scroll flow (so it never
// covers the last rows) and stays visible above the overlay, which would
// otherwise hide a viewport-fixed bar. There's never two.
let stickyBars = 0;
const barListeners = new Set<() => void>();
function setStickyBars(delta: number) {
  stickyBars += delta;
  for (const l of barListeners) l();
}
function useStickyBarMounted(): boolean {
  return useSyncExternalStore(
    (fn) => {
      barListeners.add(fn);
      return () => barListeners.delete(fn);
    },
    () => stickyBars > 0,
    () => false,
  );
}

// --staged-bar-space is the room the fixed bar takes at the bottom of the
// viewport (0 while it's hidden). Scroll containers it can overlap pad
// their end by it: pb-[var(--staged-bar-space,0px)].
const SPACE_VAR = "--staged-bar-space";
function useReserveSpace(el: HTMLElement | null) {
  useEffect(() => {
    if (!el) return;
    const root = document.documentElement;
    const apply = () => {
      const r = el.getBoundingClientRect();
      // Height plus its offset from the viewport bottom plus a gap.
      root.style.setProperty(SPACE_VAR, `${Math.ceil(window.innerHeight - r.top + 12)}px`);
    };
    apply();
    const ro = new ResizeObserver(apply);
    ro.observe(el);
    window.addEventListener("resize", apply);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", apply);
      root.style.removeProperty(SPACE_VAR);
    };
  }, [el]);
}

// StagedChangesBar is the one place staged edits get deployed: "3 changes
// across 2 services · Review · Discard all". Mount it once per surface
// that shows a project (canvas, service page, service panel). It renders
// nothing while nothing is staged.
//
// placement="fixed" pins it to the bottom of the viewport (page shells);
// "sticky" keeps it at the bottom of the nearest scroll container (inside
// a panel).
export function StagedChangesBar({
  project,
  placement = "fixed",
  className,
}: {
  project: string;
  placement?: "fixed" | "sticky";
  className?: string;
}) {
  const staged = useStagedProject(project);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const [reviewOpen, setReviewOpen] = useState(false);
  const panelHasBar = useStickyBarMounted();
  const hidden = placement === "fixed" && panelHasBar;
  useEffect(() => {
    if (placement !== "sticky") return;
    setStickyBars(1);
    return () => setStickyBars(-1);
  }, [placement]);
  const [fixedEl, setFixedEl] = useState<HTMLDivElement | null>(null);
  const [barEl, setBarEl] = useState<HTMLDivElement | null>(null);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const services = Object.keys(staged ?? {}).sort();
  // The same count the review shows (rows, not raw form fields).
  const svcQueries = useQueries({
    queries: services.map((s) => ({
      queryKey: serviceQueryKey(project, s),
      queryFn: () => getService(project, s),
      staleTime: 30_000,
    })),
  });
  const counts = services.map((s, i) => reviewItemCount(svcQueries[i]?.data, staged?.[s]));
  const changes = counts.reduce((n, c) => n + c, 0);
  const changedServices = services.filter((_, i) => (counts[i] ?? 0) > 0);
  useWarnOnUnload(project);
  const failed = services.some((s) => Object.keys(staged?.[s]?.errors ?? {}).length > 0);

  // ⌘⇧Enter / Ctrl⇧Enter opens the review from anywhere on the page.
  useEffect(() => {
    if (changes === 0 || !canWrite || hidden) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key === "Enter") {
        e.preventDefault();
        setReviewOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [changes, canWrite, hidden]);

  useReserveSpace(placement === "fixed" && !hidden && changes > 0 ? fixedEl : null);
  useKeepFocusClear(!hidden && changes > 0 ? barEl : null);
  useAnnounceChange(String(changes), (n) => (n === "0" ? "No staged changes" : `${n} staged ${n === "1" ? "change" : "changes"}`), !hidden);

  if (hidden || (changes === 0 && !reviewOpen)) return null;
  return (
    <>
      {changes > 0 && (
        <div
          ref={placement === "fixed" ? setFixedEl : undefined}
          className={cn(
            "pointer-events-none z-30 flex justify-center px-3",
            // On phones the fixed bar clears the bottom nav and the service
            // page's action bar above it instead of covering their buttons.
            placement === "fixed"
              ? "fixed inset-x-0 bottom-[calc(7rem+env(safe-area-inset-bottom))] sm:bottom-4"
              : "sticky bottom-3",
            className,
          )}
        >
          <div
            ref={setBarEl}
            role="region"
            aria-label="Staged changes"
            className="pointer-events-auto flex max-w-full flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-[var(--accent)]/40 bg-[var(--bg-elevated)] py-2 pr-2 pl-3 shadow-[var(--shadow-lg)]"
          >
            <span className="inline-flex min-w-0 items-center gap-2 text-[13px] text-[var(--text-primary)]">
              <span
                aria-hidden
                className={cn("h-2 w-2 shrink-0 rounded-full", failed ? "bg-[var(--error)]" : "bg-[var(--accent)]")}
              />
              <span>
                <span className="font-medium">
                  {changes} staged {changes === 1 ? "change" : "changes"}
                </span>
                {changedServices.length > 1 && (
                  <span className="text-[var(--text-secondary)]"> across {changedServices.length} services</span>
                )}
                {changedServices.length === 1 && <span className="text-[var(--text-secondary)]"> to {changedServices[0]}</span>}
                {failed && <span className="text-[var(--error)]"> · some failed</span>}
              </span>
            </span>
            {canWrite ? (
              <div className="flex items-center gap-1.5">
                <Button variant="ghost" size="sm" onClick={() => setConfirmDiscard(true)}>
                  Discard all
                </Button>
                <Button size="sm" onClick={() => setReviewOpen(true)} title="Review and deploy (⌘⇧↵)">
                  Review and deploy
                </Button>
              </div>
            ) : (
              <span className="text-xs text-[var(--text-secondary)]">Read-only for your role</span>
            )}
          </div>
        </div>
      )}
      <StagedReviewDialog project={project} open={reviewOpen} onOpenChange={setReviewOpen} />
      <ConfirmDialog
        open={confirmDiscard}
        title="Discard all staged changes?"
        body={`${changes} ${changes === 1 ? "change" : "changes"} across ${changedServices.length} ${changedServices.length === 1 ? "service" : "services"} will be dropped. Nothing deployed changes.`}
        confirmLabel="Discard all"
        destructive
        onCancel={() => setConfirmDiscard(false)}
        onConfirm={() => {
          discardProject(project);
          setConfirmDiscard(false);
        }}
      />
    </>
  );
}

// useKeepFocusClear scrolls the focused control up when the bar sits on
// top of it: the bar appears on the first keystroke, often right over
// the field being typed into, and later focus moves can land under it.
function useKeepFocusClear(bar: HTMLElement | null) {
  useEffect(() => {
    if (!bar) return;
    const clear = (el: Element | null) => {
      if (!(el instanceof HTMLElement) || bar.contains(el) || el === document.body) return;
      const r = el.getBoundingClientRect();
      const top = bar.getBoundingClientRect().top - 12;
      if (r.bottom <= top || r.top >= bar.getBoundingClientRect().bottom) return;
      let scroller: HTMLElement | null = el.parentElement;
      while (scroller && !/(auto|scroll)/.test(getComputedStyle(scroller).overflowY)) scroller = scroller.parentElement;
      const delta = r.bottom - top;
      if (scroller) scroller.scrollBy({ top: delta });
      else window.scrollBy({ top: delta });
    };
    clear(document.activeElement);
    const onFocus = (e: FocusEvent) => clear(e.target as Element | null);
    document.addEventListener("focusin", onFocus);
    return () => document.removeEventListener("focusin", onFocus);
  }, [bar]);
}

// useWarnOnUnload asks before a reload or tab close would drop staged
// edits. Most survive a reload (sessionStorage); secret values and
// write-only fields are never stored, so those would be lost.
function useWarnOnUnload(project: string) {
  const staged = useStagedProject(project);
  const atRisk = !!staged && hasUnpersistedStaged(project);
  useEffect(() => {
    if (!atRisk) return;
    const onUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      // Chrome < 119 needs returnValue set.
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, [atRisk]);
}
