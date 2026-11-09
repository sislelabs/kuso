"use client";

import type { DirtyPanel } from "../overlay/leaveGuard";

// SaveBar is the floating "Unsaved changes · Discard · Save" pill the
// service and addon workspaces render for the active form. It sits above
// the panel scroll so Save is one click however far the user scrolled.
export function SaveBar({ entry }: { entry: DirtyPanel | undefined }) {
  if (!entry || !entry.dirty || !entry.onSave) return null;
  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-3 z-30 flex justify-center px-3">
      <div className="pointer-events-auto flex max-w-[90%] flex-col gap-1 rounded-md border border-[var(--border-strong)] bg-[var(--bg-elevated)] px-3 py-2 shadow-[var(--shadow-lg)]">
        <div className="flex items-center gap-3">
          <span className="text-xs text-[var(--text-secondary)]">Unsaved changes</span>
          {entry.onDiscard && (
            <button
              type="button"
              onClick={() => entry.onDiscard?.()}
              disabled={entry.saving}
              className="text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] disabled:opacity-50"
            >
              Discard
            </button>
          )}
          <button
            type="button"
            onClick={() => {
              // A throwing or rejecting onSave must not leave the bar stuck;
              // the panel owns surfacing saveError.
              try {
                const r = entry.onSave?.();
                if (r && typeof (r as Promise<unknown>).then === "function") {
                  (r as Promise<unknown>).catch(() => {});
                }
              } catch {
                // already surfaced by the panel
              }
            }}
            disabled={entry.saving}
            className="inline-flex h-7 items-center rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] hover:bg-[var(--btn-primary-bg-hover)] disabled:opacity-60"
          >
            {entry.saving ? "Saving…" : "Save"}
          </button>
        </div>
        {entry.saveError && (
          // Sticky inline error: a toast disappears in 4s and leaves a
          // dirty form with no clue why the save failed.
          <p role="alert" className="max-w-[40ch] truncate text-xs text-[var(--error)]" title={entry.saveError}>
            ✗ {entry.saveError}
          </p>
        )}
      </div>
    </div>
  );
}
