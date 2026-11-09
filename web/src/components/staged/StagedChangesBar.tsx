"use client";

import { useEffect, useState, useSyncExternalStore } from "react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useCanOnProject, Perms } from "@/features/auth";
import { countServiceChanges, discardProject, useStagedProject } from "@/features/staged";
import { cn } from "@/lib/utils";
import { StagedReviewDialog } from "./StagedReviewDialog";

// Page shells mount a fixed bar; the sticky fallback inside the settings
// and variables panels hides while one is mounted, so there's never two.
let fixedBars = 0;
const barListeners = new Set<() => void>();
function setFixedBars(delta: number) {
  fixedBars += delta;
  for (const l of barListeners) l();
}
function useFixedBarMounted(): boolean {
  return useSyncExternalStore(
    (fn) => {
      barListeners.add(fn);
      return () => barListeners.delete(fn);
    },
    () => fixedBars > 0,
    () => false,
  );
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
  const shellHasBar = useFixedBarMounted();
  const hidden = placement === "sticky" && shellHasBar;
  useEffect(() => {
    if (placement !== "fixed") return;
    setFixedBars(1);
    return () => setFixedBars(-1);
  }, [placement]);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const services = Object.keys(staged ?? {});
  const changes = services.reduce((n, s) => n + countServiceChanges(staged?.[s]), 0);
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

  if (hidden || (changes === 0 && !reviewOpen)) return null;
  return (
    <>
      {changes > 0 && (
        <div
          className={cn(
            "pointer-events-none z-30 flex justify-center px-3",
            placement === "fixed" ? "fixed inset-x-0 bottom-4" : "sticky bottom-3",
            className,
          )}
        >
          <div
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
                {services.length > 1 && (
                  <span className="text-[var(--text-secondary)]"> across {services.length} services</span>
                )}
                {services.length === 1 && <span className="text-[var(--text-secondary)]"> to {services[0]}</span>}
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
        body={`${changes} ${changes === 1 ? "change" : "changes"} across ${services.length} ${services.length === 1 ? "service" : "services"} will be dropped. Nothing deployed changes.`}
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
