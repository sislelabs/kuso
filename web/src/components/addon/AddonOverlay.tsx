"use client";

import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useAddons } from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { useCanOnProject, Perms } from "@/features/auth";
import { X, Database, HardDrive, Settings, Info, ExternalLink } from "lucide-react";
import { cn } from "@/lib/utils";

import { OverviewTab } from "./overlay/OverviewTab";
import { BackupsTab } from "./overlay/BackupsTab";
import { SQLTab } from "./overlay/SQLTab";
import { SettingsTab } from "./overlay/SettingsTab";

// AddonOverlayDirtyContext mirrors the per-panel dirty registry on
// ServiceOverlay. Each form section inside a tab (Configuration,
// Placement, Resync …) calls useAddonOverlayDirty to register its
// dirty state + onSave/onDiscard so the shell can render one
// unified SaveBar at the bottom and prompt before tab-switch
// discards an unsaved edit. Without this the user could switch
// from Configuration → Backups → back and the typed values were
// gone with no warning.
type PanelEntry = {
  dirty: boolean;
  onSave?: () => unknown;
  onDiscard?: () => void;
  saving?: boolean;
  saveError?: string;
};
type AddonOverlayDirtyAPI = {
  setPanel: (key: string, entry: PanelEntry) => void;
  clearPanel: (key: string) => void;
};
const AddonOverlayDirtyContext = createContext<AddonOverlayDirtyAPI | null>(null);
export function useAddonOverlayDirty(
  panelKey: string,
  dirty: boolean,
  opts?: {
    // unknown return is intentional — callers commonly pass a
    // useMutation().mutateAsync() which returns the typed response.
    // We only care that the call fired; the shell catches a rejected
    // promise to prevent the SaveBar getting stuck.
    onSave?: () => unknown;
    onDiscard?: () => void;
    saving?: boolean;
    saveError?: string;
  }
) {
  const api = useContext(AddonOverlayDirtyContext);
  useEffect(() => {
    if (!api) return;
    api.setPanel(panelKey, {
      dirty,
      onSave: opts?.onSave,
      onDiscard: opts?.onDiscard,
      saving: opts?.saving,
      saveError: opts?.saveError,
    });
    return () => api.clearPanel(panelKey);
  }, [api, panelKey, dirty, opts?.onSave, opts?.onDiscard, opts?.saving, opts?.saveError]);
}

type Tab = "overview" | "backups" | "sql" | "settings";
const TABS: { id: Tab; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { id: "overview", label: "Overview", icon: Info },
  { id: "backups",  label: "Backups",  icon: HardDrive },
  { id: "sql",      label: "SQL",      icon: Database },
  { id: "settings", label: "Settings", icon: Settings },
];

interface Props {
  project: string;
  addon: string | null;
  // Lets the canvas right-click "SQL console" / "Backups + restore"
  // entries open the right tab directly. Falls back to "overview".
  defaultTab?: string;
  onClose: () => void;
}

// AddonOverlay mirrors ServiceOverlay: a right-side slide-in panel
// scoped to one addon, with tabs for the operations the user
// actually does on a postgres (browse, restore, delete). Backups +
// SQL tabs hide themselves for non-postgres addons since the
// endpoints they hit are postgres-only today.
//
// Per-tab implementations live under ./overlay/. This file is the
// shell: open/close, tab gating, the slide-in chrome.
export function AddonOverlay({ project, addon, defaultTab, onClose }: Props) {
  const open = !!addon;
  const [tab, setTab] = useState<Tab>("overview");
  const panelRef = useRef<HTMLDivElement>(null);

  // Per-panel dirty + save registry. The active tab's onSave wires
  // into the floating SaveBar below; on tab-switch (or close) we
  // run each panel's onDiscard so transient form state doesn't
  // silently survive.
  const [panels, setPanels] = useState<Record<string, PanelEntry>>({});
  const dirtyAPI = useMemo<AddonOverlayDirtyAPI>(
    () => ({
      setPanel: (key, entry) => {
        setPanels((prev) => ({ ...prev, [key]: entry }));
      },
      clearPanel: (key) => {
        setPanels((prev) => {
          const next = { ...prev };
          delete next[key];
          return next;
        });
      },
    }),
    []
  );

  // Tab-switch / close discards local form state cleanly. Without
  // this the SettingsTab forms held edited values across a
  // Configuration → Backups → Configuration round-trip and the
  // user couldn't tell whether their edits had been saved. We don't
  // prompt: the floating SaveBar's persistent "unsaved changes"
  // indicator already telegraphs the state and corrections are
  // cheap. ServiceOverlay made the same call.
  const guardedSetTab = (next: Tab) => {
    if (next === tab) return;
    for (const e of Object.values(panels)) {
      e.onDiscard?.();
    }
    setTab(next);
  };
  const guardedClose = () => {
    for (const e of Object.values(panels)) {
      e.onDiscard?.();
    }
    setPanels({});
    onClose();
  };

  useEffect(() => {
    if (!addon) return;
    const valid = TABS.some((t) => t.id === defaultTab);
    setTab(valid ? (defaultTab as Tab) : "overview");
    setPanels({});
  }, [addon, defaultTab]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") guardedClose();
    };
    window.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, onClose]);

  const addons = useAddons(project);
  const data = (addons.data ?? []).find((a) => a.metadata.name === addon);
  const kind = data?.spec.kind ?? "";
  const isPostgres = kind === "postgres";
  const canSQL = useCanOnProject(project, Perms.SQLRead);
  const canWriteAddon = useCanOnProject(project, Perms.AddonsWrite);

  return (
    <AnimatePresence>
      {open && (
        <div className="fixed inset-0 z-50 flex" role="dialog" aria-modal="true">
          <motion.button
            type="button"
            aria-label="Close"
            onClick={guardedClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="absolute inset-0 bg-[rgba(8,8,11,0.55)] backdrop-blur-[2px]"
          />
          <motion.div
            ref={panelRef}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            className="relative z-10 ml-auto flex h-full w-full max-w-3xl flex-col border-l border-[var(--border-subtle)] bg-[var(--bg-primary)] shadow-[var(--shadow-lg)]"
          >
            <header className="flex shrink-0 items-start gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-5 py-4">
              <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-primary)]">
                <AddonIcon kind={kind} />
              </span>
              <div className="min-w-0 flex-1">
                <h2 className="font-heading text-lg font-semibold tracking-tight truncate">
                  {addon ?? ""}
                </h2>
                <div className="mt-1 flex flex-wrap items-center gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
                  <span className="uppercase tracking-widest">{addonLabel(kind)}</span>
                  <span>·</span>
                  <span>project {project}</span>
                </div>
              </div>
              {/* Open Web UI: only renders when the addon's spec.webUI
                  is enabled (the kuso server only proxies kinds with
                  a known UI port, mailpit/nats today). The endpoint
                  itself requires an authenticated kuso session, so
                  the new-tab open inherits whatever cookie/session
                  the dashboard is already using. */}
              {data?.spec.webUI?.enabled && (
                <a
                  href={`/api/projects/${project}/addons/${addon}/webui/`}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2.5 text-[11px] font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] hover:text-[var(--text-primary)]"
                  title="Open the addon's built-in web console"
                >
                  Open Web UI
                  <ExternalLink className="h-3 w-3" />
                </a>
              )}
              <button
                type="button"
                onClick={guardedClose}
                aria-label="Close"
                className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-4 w-4" />
              </button>
            </header>

            <nav className="flex shrink-0 items-center gap-1 border-b border-[var(--border-subtle)] px-3">
              {TABS.map((t) => {
                if (!isPostgres && (t.id === "backups" || t.id === "sql")) return null;
                // SQL tab needs sql:read; Settings (delete) needs
                // addons:write. Hide entirely for users without —
                // less confusing than rendering then 403'ing on action.
                if (t.id === "sql" && !canSQL) return null;
                if (t.id === "settings" && !canWriteAddon) return null;
                const active = t.id === tab;
                return (
                  <button
                    key={t.id}
                    type="button"
                    onClick={() => guardedSetTab(t.id)}
                    className={cn(
                      "relative inline-flex h-10 items-center gap-1.5 px-3 text-sm font-medium transition-colors",
                      active
                        ? "text-[var(--text-primary)]"
                        : "text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                    )}
                  >
                    <t.icon className="h-3.5 w-3.5" />
                    {t.label}
                    {active && (
                      <motion.span
                        layoutId="addon-tab-underline"
                        className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--text-primary)]"
                        transition={{ type: "spring", stiffness: 380, damping: 32 }}
                      />
                    )}
                  </button>
                );
              })}
            </nav>

            <div className="relative min-h-0 flex-1 overflow-y-auto">
              <AddonOverlayDirtyContext.Provider value={dirtyAPI}>
                {!data ? (
                  <div className="space-y-3 p-6">
                    <Skeleton className="h-8 w-48" />
                    <Skeleton className="h-32 w-full" />
                  </div>
                ) : tab === "overview" ? (
                  <OverviewTab project={project} addon={addon!} kind={kind} cr={data} />
                ) : tab === "backups" ? (
                  <BackupsTab project={project} addon={addon!} />
                ) : tab === "sql" ? (
                  <SQLTab project={project} addon={addon!} />
                ) : (
                  <SettingsTab project={project} addon={addon!} cr={data} onClose={guardedClose} />
                )}
                {/* Unified SaveBar: any panel that registered onSave
                    with useAddonOverlayDirty surfaces here. Multiple
                    forms on one tab (Configuration + Placement +
                    Resync on Settings) each render their own pill
                    keyed by panelKey — pick the first dirty one. */}
                {(() => {
                  const active = Object.entries(panels).find(([, e]) => e.dirty && e.onSave);
                  if (!active) return null;
                  const [, entry] = active;
                  return (
                    <div className="pointer-events-none absolute inset-x-0 bottom-3 z-30 flex justify-center px-3">
                      <div className="pointer-events-auto flex max-w-[90%] flex-col gap-1 rounded-md border border-[var(--border-strong)] bg-[var(--bg-elevated)] px-3 py-2 shadow-[var(--shadow-lg)]">
                        <div className="flex items-center gap-3">
                          <span className="font-mono text-[11px] text-[var(--text-secondary)]">
                            unsaved changes
                          </span>
                          {entry.onDiscard && (
                            <button
                              type="button"
                              onClick={() => entry.onDiscard?.()}
                              disabled={entry.saving}
                              className="font-mono text-[11px] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
                            >
                              discard
                            </button>
                          )}
                          <button
                            type="button"
                            onClick={() => {
                              try {
                                const r = entry.onSave?.();
                                if (r && typeof (r as Promise<unknown>).then === "function") {
                                  (r as Promise<unknown>).catch(() => {});
                                }
                              } catch {
                                /* surfaced by panel */
                              }
                            }}
                            disabled={entry.saving}
                            className="inline-flex h-7 items-center rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] hover:bg-[var(--btn-primary-bg-hover)] disabled:opacity-60"
                          >
                            {entry.saving ? "Saving…" : "Save"}
                          </button>
                        </div>
                        {entry.saveError && (
                          <p
                            role="alert"
                            className="font-mono text-[10px] text-red-300 max-w-[40ch] truncate"
                            title={entry.saveError}
                          >
                            ✗ {entry.saveError}
                          </p>
                        )}
                      </div>
                    </div>
                  );
                })()}
              </AddonOverlayDirtyContext.Provider>
            </div>
          </motion.div>
        </div>
      )}
    </AnimatePresence>
  );
}
