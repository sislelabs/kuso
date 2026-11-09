"use client";

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { claimEscape } from "@/lib/escape-layer";
import { useModalFocus } from "@/lib/use-modal-focus";

// One registered form inside an overlay (Variables, Settings, an addon
// card). onSave is optional: forms with their own review step (env
// diff, blast-radius confirm) only register dirty + discard.
export type DirtyPanel = {
  dirty: boolean;
  onSave?: () => unknown;
  onDiscard?: () => void;
  saving?: boolean;
  saveError?: string;
};

export function dirtyPanelKeys(panels: Record<string, DirtyPanel>): string[] {
  return Object.keys(panels).filter((k) => panels[k]?.dirty);
}

// "Save" in the leave dialog only makes sense when every dirty form can
// save in one step.
export function canSaveAll(panels: Record<string, DirtyPanel>): boolean {
  const keys = dirtyPanelKeys(panels);
  return keys.length > 0 && keys.every((k) => !!panels[k]?.onSave);
}

const PANEL_LABEL: Record<string, string> = {
  variables: "Variables",
  settings: "Settings",
  "addon-configuration": "Configuration",
  "addon-external-pooler": "Pooler",
  "addon-placement": "Placement",
};

export function dirtyPanelLabels(panels: Record<string, DirtyPanel>): string[] {
  return dirtyPanelKeys(panels).map((k) => PANEL_LABEL[k] ?? k.charAt(0).toUpperCase() + k.slice(1));
}

type Pending = { proceed: () => void; cancel?: () => void; verb: string };

// useLeaveGuard is the single exit guard both overlays use: X, backdrop,
// Escape, tab change and resource switch all go through guard(). With
// nothing dirty the action runs straight away; otherwise the dialog asks
// Keep editing / Discard / Save.
export function useLeaveGuard(
  panels: Record<string, DirtyPanel>,
  clearPanels: () => void,
) {
  const panelsRef = useRef(panels);
  panelsRef.current = panels;
  const [pending, setPending] = useState<Pending | null>(null);
  const [saving, setSaving] = useState(false);

  const discardAll = useCallback(() => {
    for (const p of Object.values(panelsRef.current)) p.onDiscard?.();
    clearPanels();
  }, [clearPanels]);

  const guard = useCallback(
    (verb: string, proceed: () => void, cancel?: () => void) => {
      if (dirtyPanelKeys(panelsRef.current).length === 0) {
        proceed();
        return;
      }
      setPending({ proceed, cancel, verb });
    },
    [],
  );

  const keep = () => {
    pending?.cancel?.();
    setPending(null);
  };
  const discard = () => {
    const p = pending;
    setPending(null);
    discardAll();
    p?.proceed();
  };
  const save = async () => {
    const p = pending;
    const current = panelsRef.current;
    setSaving(true);
    try {
      await Promise.all(dirtyPanelKeys(current).map((k) => current[k]?.onSave?.()));
      setPending(null);
      clearPanels();
      p?.proceed();
    } catch {
      // The form shows its own save error; stay put so nothing is lost.
      setPending(null);
      p?.cancel?.();
    } finally {
      setSaving(false);
    }
  };

  const dialog = (
    <UnsavedChangesDialog
      open={!!pending}
      verb={pending?.verb ?? "leaving"}
      sections={dirtyPanelLabels(panels)}
      canSave={canSaveAll(panels)}
      saving={saving}
      onKeep={keep}
      onDiscard={discard}
      onSave={() => void save()}
    />
  );
  return { guard, dialog, discardAll };
}

function UnsavedChangesDialog({
  open,
  verb,
  sections,
  canSave,
  saving,
  onKeep,
  onDiscard,
  onSave,
}: {
  open: boolean;
  verb: string;
  sections: string[];
  canSave: boolean;
  saving: boolean;
  onKeep: () => void;
  onDiscard: () => void;
  onSave: () => void;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useModalFocus(open, panelRef);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      claimEscape(e);
      onKeep();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [open, onKeep]);
  if (!open) return null;
  const what = sections.length > 0 ? sections.join(" and ") : "this panel";
  return (
    <div
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px] dark:bg-black/60"
      onClick={onKeep}
    >
      <div
        ref={panelRef}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={`${titleId}-body`}
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md overflow-hidden rounded-xl border border-[var(--border-default)] bg-[var(--popover)] shadow-[var(--shadow-lg)]"
      >
        <div className="flex items-start gap-2.5 px-5 pt-5 pb-4">
          <span aria-hidden className="flex size-7 shrink-0 items-center justify-center rounded-md bg-[var(--warning-subtle)]">
            <AlertCircle className="size-4 text-[var(--warning)]" />
          </span>
          <div className="min-w-0">
            <h2 id={titleId} className="text-base font-semibold tracking-tight text-[var(--text-primary)]">
              Unsaved changes
            </h2>
            <p id={`${titleId}-body`} className="mt-1 text-sm text-[var(--text-secondary)]">
              Your edits in {what} haven&apos;t been saved. {verb.charAt(0).toUpperCase() + verb.slice(1)} drops them
              {canSave ? " unless you save first" : ""}.
            </p>
          </div>
        </div>
        <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-5 py-3">
          <Button type="button" variant="secondary" size="sm" onClick={onKeep} disabled={saving}>
            Keep editing
          </Button>
          <Button type="button" variant="destructive-subtle" size="sm" onClick={onDiscard} disabled={saving}>
            Discard
          </Button>
          {canSave && (
            <Button type="button" variant="primary" size="sm" onClick={onSave} loading={saving}>
              Save
            </Button>
          )}
        </footer>
      </div>
    </div>
  );
}
