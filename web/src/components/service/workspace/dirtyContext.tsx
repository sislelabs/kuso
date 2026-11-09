"use client";

import { createContext, useContext, useEffect, useRef } from "react";
import type { DirtyPanel } from "../overlay/leaveGuard";

// Per-panel dirty + save registry shared by every panel inside a
// ServiceWorkspace (peek or full page). Panels register whether their
// form has unsaved edits and, optionally, the save + discard handlers
// the workspace's unified SaveBar fires.
export type OverlayDirtyAPI = {
  setPanel: (key: string, entry: DirtyPanel) => void;
  clearPanel: (key: string) => void;
};

export const OverlayDirtyContext = createContext<OverlayDirtyAPI | null>(null);

export function useOverlayDirty(
  panelKey: string,
  dirty: boolean,
  opts?: {
    onSave?: () => void | Promise<void>;
    onDiscard?: () => void;
    saving?: boolean;
    saveError?: string;
  },
) {
  const api = useContext(OverlayDirtyContext);
  // Panels pass fresh closures every render. Registering those directly
  // made the effect re-run on every render, and each run sets workspace
  // state, which re-renders the panel: an infinite update loop. The
  // registered handlers read the latest closure through refs instead.
  const onSaveRef = useRef(opts?.onSave);
  const onDiscardRef = useRef(opts?.onDiscard);
  useEffect(() => {
    onSaveRef.current = opts?.onSave;
    onDiscardRef.current = opts?.onDiscard;
  });
  const hasSave = !!opts?.onSave;
  const hasDiscard = !!opts?.onDiscard;
  useEffect(() => {
    if (!api) return;
    api.setPanel(panelKey, {
      dirty,
      onSave: hasSave ? () => onSaveRef.current?.() : undefined,
      onDiscard: hasDiscard ? () => onDiscardRef.current?.() : undefined,
      saving: opts?.saving,
      saveError: opts?.saveError,
    });
    return () => api.clearPanel(panelKey);
  }, [api, panelKey, dirty, hasSave, hasDiscard, opts?.saving, opts?.saveError]);
}
