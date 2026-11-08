"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import Link from "next/link";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useAddons } from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { useCanOnProject, Perms } from "@/features/auth";
import { X, ExternalLink, Plug, DatabaseBackup } from "lucide-react";
import { StatusPill } from "@/components/ui/status-pill";
import {
  useAddonHealth,
  useAddonBackups,
  useBackupNow,
  backupNowState,
  backupsApplicable,
  backupsListState,
} from "@/features/addons";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { KusoAddon } from "@/types/projects";
import { serviceShortName } from "@/lib/utils";
import { escapeHandledAbove } from "@/lib/escape-layer";
import { useUnsavedEdits } from "@/lib/unsaved-edits";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { QueryErrorState } from "@/components/shared/QueryErrorState";

import { OverviewTab } from "./overlay/OverviewTab";
import { BackupsTab } from "./overlay/BackupsTab";
import { SQLTab } from "./overlay/SQLTab";
import { SettingsTab } from "./overlay/SettingsTab";
import { VulnerabilitiesTab } from "./overlay/VulnerabilitiesTab";
import { OverlayTabs, type OverlayTabItem } from "@/components/service/overlay/OverlayTabs";
import { useOverlayKeyboard } from "@/components/service/overlay/useOverlayKeyboard";

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
  // Same loop guard as useOverlayDirty in ServiceOverlay: callers pass
  // fresh closures every render, and registering them directly re-ran
  // the effect → setPanel → re-render forever. Read them through refs.
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

type Tab = "overview" | "backups" | "sql" | "vulnerabilities" | "settings";
// No icons: same tab language as the service overlay.
const TABS: OverlayTabItem<Tab>[] = [
  { id: "overview", label: "Overview", chord: "o" },
  { id: "backups", label: "Backups", chord: "b" },
  { id: "sql", label: "Data", chord: "q" },
  { id: "vulnerabilities", label: "Vulnerabilities", chord: "u" },
  { id: "settings", label: "Settings", chord: "s" },
];

interface Props {
  project: string;
  addon: string | null;
  // Lets the canvas right-click "SQL console" / "Backups + restore"
  // entries open the right tab directly. Falls back to "overview".
  defaultTab?: string;
  // Called when the user switches tabs (and once on open when no tab was
  // requested) so the page can mirror the tab into the URL.
  onTabChange?: (tab: string) => void;
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
export function AddonOverlay({ project, addon, defaultTab, onTabChange, onClose }: Props) {
  const open = !!addon;
  const [tab, setTab] = useState<Tab>("overview");
  const panelRef = useRef<HTMLDivElement>(null);

  // Per-panel dirty + save registry. The active tab's onSave wires
  // into the floating SaveBar below; on tab-switch (or close) we
  // run each panel's onDiscard so transient form state doesn't
  // silently survive.
  const [panels, setPanels] = useState<Record<string, PanelEntry>>({});
  useUnsavedEdits("addon-overlay", Object.values(panels).some((p) => p.dirty));
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
    onTabChange?.(next);
  };
  const guardedClose = useCallback(() => {
    for (const e of Object.values(panels)) {
      e.onDiscard?.();
    }
    setPanels({});
    onClose();
  }, [panels, onClose]);
  // Escape with unsaved edits asks first; backdrop / X still discard.
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // The URL is the source of truth for the tab: defaultTab changes on
  // every tab switch (the page mirrors it into ?tab=) and on Back/Forward.
  // Panels are only reset when the addon itself changes — resetting on a
  // tab change would wipe the registration the new tab just made.
  useEffect(() => {
    setPanels({});
  }, [addon]);
  const onTabChangeRef = useRef(onTabChange);
  onTabChangeRef.current = onTabChange;
  useEffect(() => {
    if (!addon) return;
    const requested = defaultTab === "vulns" ? "vulnerabilities" : defaultTab;
    const valid = TABS.some((t) => t.id === requested);
    setTab(valid ? (requested as Tab) : "overview");
    if (!defaultTab) onTabChangeRef.current?.("overview");
  }, [addon, defaultTab]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // A dialog/menu opened from inside the overlay (restore confirm,
      // etc.) handles its own Escape; don't also close the overlay under it.
      if (escapeHandledAbove(e, rootRef.current)) return;
      if (Object.values(panels).some((p) => p.dirty)) {
        setConfirmDiscard(true);
        return;
      }
      guardedClose();
    };
    window.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, panels, guardedClose]);

  const addons = useAddons(project);
  const data = (addons.data ?? []).find((a) => a.metadata.name === addon);
  const kind = data?.spec.kind ?? "";
  const isPostgres = kind === "postgres";
  // The SQL browser/data-editor works against any postgres-wire addon —
  // both the single-node and HA charts. (Backups stays postgres-only for
  // now; its restore path is chart-specific.)
  const isSQLCapable = kind === "postgres" || kind === "postgres-ha";
  const canSQL = useCanOnProject(project, Perms.SQLRead);
  const canWriteAddon = useCanOnProject(project, Perms.AddonsWrite);
  // Backups and restore are postgres-only today; SQL needs a postgres-wire
  // addon and sql:read; Settings (delete) needs addons:write.
  const visibleTabs = TABS.filter((t) => {
    if (t.id === "backups") return isPostgres;
    if (t.id === "sql") return isSQLCapable && canSQL;
    if (t.id === "settings") return canWriteAddon;
    return true;
  });
  useOverlayKeyboard({
    open,
    panelRef,
    restoreKey: addon ? `addon:${addon}` : undefined,
    chords: Object.fromEntries(visibleTabs.map((t) => [t.chord as string, () => guardedSetTab(t.id)])),
  });

  return (
    <AnimatePresence>
      {open && (
        <div ref={rootRef} className="fixed inset-0 z-50 flex" role="dialog" aria-modal="true" aria-label={`Addon ${addon ?? ""}`}>
          <motion.button
            type="button"
            aria-label="Close"
            onClick={guardedClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="absolute inset-0 bg-[var(--scrim)] backdrop-blur-[2px] lg:bg-[var(--scrim-wide)] lg:backdrop-blur-none"
          />
          <motion.div
            ref={panelRef}
            tabIndex={-1}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            className="relative z-10 ml-auto flex h-full w-full flex-col bg-[var(--bg-primary)] shadow-[var(--shadow-lg)] outline-none sm:max-w-3xl sm:border-l sm:border-[var(--border-subtle)]"
          >
            <AddonHeader
              project={project}
              addon={addon ?? ""}
              kind={kind}
              cr={data}
              onConnect={() => {
                guardedSetTab("overview");
                // The overview mounts on the next frame after a tab switch.
                requestAnimationFrame(() =>
                  requestAnimationFrame(() =>
                    document.getElementById("addon-connection")?.scrollIntoView({ behavior: smoothScroll(), block: "start" }),
                  ),
                );
              }}
              onOpenBackups={() => guardedSetTab("backups")}
              onClose={guardedClose}
            />

            <OverlayTabs
              tabs={visibleTabs.filter((t) => t.id !== "settings")}
              pinned={visibleTabs.find((t) => t.id === "settings")}
              active={tab}
              onChange={guardedSetTab}
              idPrefix="addon-overlay"
              label="Addon sections"
            />

            <div
              id="addon-overlay-panel"
              role="tabpanel"
              aria-labelledby={`addon-overlay-tab-${tab}`}
              className="relative min-h-0 flex-1 overflow-y-auto"
            >
              <AddonOverlayDirtyContext.Provider value={dirtyAPI}>
                {!data && addons.isError ? (
                  <div className="p-6">
                    <QueryErrorState
                      what="addon"
                      error={addons.error}
                      onRetry={() => void addons.refetch()}
                    />
                  </div>
                ) : !data ? (
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
                ) : tab === "vulnerabilities" ? (
                  <VulnerabilitiesTab key={addon} project={project} addon={addon!} />
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
                          <span className="text-xs text-[var(--text-secondary)]">
                            Unsaved changes
                          </span>
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
                            className="max-w-[40ch] truncate text-xs text-[var(--error)]"
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
          <ConfirmDialog
            open={confirmDiscard}
            title="Discard unsaved changes?"
            body="This addon has edits that haven't been saved. Closing drops them."
            confirmLabel="Discard"
            onCancel={() => setConfirmDiscard(false)}
            onConfirm={() => {
              setConfirmDiscard(false);
              guardedClose();
            }}
          />
        </div>
      )}
    </AnimatePresence>
  );
}

const headerButtonClass =
  "inline-flex h-7 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2.5 text-xs font-medium text-[var(--text-secondary)] hover:border-[var(--border-strong)] hover:text-[var(--text-primary)] disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 aria-disabled:hover:border-[var(--border-subtle)] aria-disabled:hover:text-[var(--text-secondary)]";

// AddonHeader matches the service overlay's header: icon, name + status
// pill, a context line, then the actions you reach for on a datastore.
function AddonHeader({
  project,
  addon,
  kind,
  cr,
  onConnect,
  onOpenBackups,
  onClose,
}: {
  project: string;
  addon: string;
  kind: string;
  cr?: KusoAddon;
  onConnect: () => void;
  onOpenBackups: () => void;
  onClose: () => void;
}) {
  const health = useAddonHealth(project, addon, cr);
  const version = cr?.spec.version;
  const publicPort = cr?.spec.publicTCP?.enabled ? cr.spec.publicTCP.port : undefined;
  // Same naming as the canvas and the service overlay: the short name is
  // the title, the real resource name sits underneath.
  const displayName = serviceShortName(project, addon);
  return (
    <header className="flex shrink-0 items-start gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-3 sm:gap-3 sm:px-5 sm:py-4">
      <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-primary)]">
        <AddonIcon kind={kind} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <h2 className="truncate font-heading text-lg font-semibold tracking-tight" title={addon}>
            {displayName}
          </h2>
          {cr && <StatusPill status={health.status} detail={health.detail} />}
        </div>
        <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--text-secondary)]">
          <span className="min-w-0 truncate">
            {project}
            <span aria-hidden className="px-1 text-[var(--text-tertiary)]">/</span>
            <span className="font-mono">{addon}</span>
            <span aria-hidden className="px-1 text-[var(--text-tertiary)]">·</span>
            <span>
              {addonLabel(kind)}
              {version ? ` ${version}` : ""}
            </span>
          </span>
          {publicPort ? (
            <span className="rounded border border-[var(--warning)]/40 px-1.5 py-px text-[var(--warning)]">Public · port {publicPort}</span>
          ) : cr && !cr.spec.external ? (
            <span className="text-[var(--text-tertiary)]">Private network only</span>
          ) : null}
        </div>
        <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
          <button type="button" onClick={onConnect} className={headerButtonClass}>
            <Plug className="h-3 w-3" aria-hidden />
            Connect
          </button>
          {backupsApplicable(cr) && (
            <BackupNowButton project={project} addon={addon} cr={cr} onOpenBackups={onOpenBackups} />
          )}
          {/* Only addons whose spec.webUI is on (mailpit/nats today); the
              proxy requires the dashboard session. */}
          {cr?.spec.webUI?.enabled && (
            <a
              href={`/api/projects/${project}/addons/${addon}/webui/`}
              target="_blank"
              rel="noreferrer"
              title="Open the addon's built-in web console"
              className={headerButtonClass}
            >
              Open web UI
              <ExternalLink className="h-3 w-3" aria-hidden />
            </a>
          )}
        </div>
      </div>
      <button
        type="button"
        onClick={onClose}
        aria-label="Close"
        className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        <X className="h-4 w-4" />
      </button>
    </header>
  );
}

// BackupNowButton is the overlay's only "Back up now". backupNowState
// holds the rules; a blocked button stays focusable (aria-disabled) so
// hover and keyboard users can still read why.
function BackupNowButton({
  project,
  addon,
  cr,
  onOpenBackups,
}: {
  project: string;
  addon: string;
  cr?: KusoAddon;
  onOpenBackups: () => void;
}) {
  const canWrite = useCanOnProject(project, Perms.AddonsWrite);
  const list = useAddonBackups(project, addon);
  const run = useBackupNow(project, addon);
  const listState = backupsListState(list);
  const state = backupNowState(cr, { canWrite, list: listState });
  const blocked = !state.enabled || run.isPending;
  const reasonId = `backup-now-reason-${addon}`;
  // The two blockers you can fix from here get the fix inline.
  const fixable = !state.enabled && canWrite && cr?.spec.kind === "postgres" && !cr.spec.external && !cr.spec.useInstanceAddon;
  const fix = !fixable
    ? null
    : !cr?.spec.backup?.schedule?.trim()
      ? { text: "No backup schedule", action: "Set one up", onClick: onOpenBackups }
      : listState.kind === "no-s3"
        ? { text: "S3 isn't set up", action: "Configure", href: "/settings/backups" }
        : null;
  return (
    <>
      <Tooltip>
        <TooltipTrigger
          render={
            <button
              type="button"
              aria-disabled={blocked || undefined}
              aria-describedby={reasonId}
              onClick={() => {
                if (!blocked) run.mutate();
              }}
              className={headerButtonClass}
            >
              <DatabaseBackup className="h-3 w-3" aria-hidden />
              {run.isPending ? "Starting…" : "Back up now"}
              <span id={reasonId} className="sr-only">
                {state.reason}
              </span>
            </button>
          }
        />
        <TooltipContent side="bottom" className="block max-w-64 text-left">
          {state.reason}
        </TooltipContent>
      </Tooltip>
      {fix && (
        <span className="inline-flex items-center gap-1 text-xs text-[var(--text-tertiary)]">
          {fix.text}
          <span aria-hidden>·</span>
          {fix.href ? (
            <Link href={fix.href} className="font-medium text-[var(--link)] hover:underline">
              {fix.action}
            </Link>
          ) : (
            <button type="button" onClick={fix.onClick} className="font-medium text-[var(--link)] hover:underline">
              {fix.action}
            </button>
          )}
        </span>
      )}
    </>
  );
}
