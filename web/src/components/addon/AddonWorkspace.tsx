"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import Link from "next/link";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { useAddons } from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { Kbd } from "@/components/ui/kbd";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { useCanOnProject, Perms } from "@/features/auth";
import { X, ExternalLink, Plug, DatabaseBackup, Maximize2 } from "lucide-react";
import { StatusPill } from "@/components/ui/status-pill";
import {
  useAddonHealth,
  addonVersionLabel,
  useAddonBackups,
  useBackupNow,
  backupNowState,
  backupsApplicable,
  backupsListState,
} from "@/features/addons";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { KusoAddon } from "@/types/projects";
import { cn, serviceShortName } from "@/lib/utils";
import { escapeHandledAbove } from "@/lib/escape-layer";
import { useUnsavedEdits } from "@/lib/unsaved-edits";
import { QueryErrorState } from "@/components/shared/QueryErrorState";

import { OverviewTab } from "./overlay/OverviewTab";
import { BackupsTab } from "./overlay/BackupsTab";
import { SQLTab } from "./overlay/SQLTab";
import { SettingsTab } from "./overlay/SettingsTab";
import { VulnerabilitiesTab } from "./overlay/VulnerabilitiesTab";
import { OverlayTabs, type OverlayTabItem } from "@/components/service/overlay/OverlayTabs";
import { useOverlayKeyboard } from "@/components/service/overlay/useOverlayKeyboard";
import { useLeaveGuard, dirtyPanelKeys, type DirtyPanel } from "@/components/service/overlay/leaveGuard";
import { SaveBar } from "@/components/service/workspace/SaveBar";
import type { WorkspaceFrameArgs, WorkspaceVariant } from "@/components/service/workspace/ServiceWorkspace";

// Per-panel dirty registry, mirroring the service workspace: each form
// section (Configuration, Placement, Resync …) registers its dirty state
// + onSave/onDiscard so the shell renders one SaveBar and the leave guard
// asks before a tab switch or close drops typed values.
type AddonOverlayDirtyAPI = {
  setPanel: (key: string, entry: DirtyPanel) => void;
  clearPanel: (key: string) => void;
};
const AddonOverlayDirtyContext = createContext<AddonOverlayDirtyAPI | null>(null);
export function useAddonOverlayDirty(
  panelKey: string,
  dirty: boolean,
  opts?: {
    // unknown: callers commonly pass a mutateAsync() returning the typed
    // response; only whether it rejects matters here.
    onSave?: () => unknown;
    onDiscard?: () => void;
    saving?: boolean;
    saveError?: string;
  }
) {
  const api = useContext(AddonOverlayDirtyContext);
  // Callers pass fresh closures every render; read them through refs so
  // registering doesn't loop setPanel → re-render forever.
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
  variant: WorkspaceVariant;
  project: string;
  // Full CR name (<project>-<addon>).
  addon: string | null;
  // Requested tab (?tab=). Falls back to "overview".
  defaultTab?: string;
  onTabChange?: (tab: string) => void;
  // Puts the held addon back in the URL after "Keep editing" on a switch.
  onRestore?: (addon: string) => void;
  // Peek only.
  onClose?: () => void;
  onOpenPage?: (tab: string) => void;
  frame?: (args: WorkspaceFrameArgs) => React.ReactNode;
}

// AddonWorkspace is everything about one addon (header, tabs, panels,
// SaveBar, leave guard), rendered by the AddonOverlay peek and by the
// full page at /projects/<p>/addons/<a>.
export function AddonWorkspace({
  variant,
  project,
  addon: addonProp,
  defaultTab,
  onTabChange,
  onRestore,
  onClose,
  onOpenPage,
  frame,
}: Props) {
  const page = variant === "page";
  const [tab, setTab] = useState<Tab>("overview");
  const rootRef = useRef<HTMLDivElement>(null);

  const [panels, setPanels] = useState<Record<string, DirtyPanel>>({});
  const anyDirty = dirtyPanelKeys(panels).length > 0;
  useUnsavedEdits(page ? "addon-page" : "addon-overlay", anyDirty);
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
  const clearPanels = useCallback(() => setPanels({}), []);
  const { guard, dialog: leaveDialog } = useLeaveGuard(panels, clearPanels);

  // A new addon prop while a form is dirty keeps the current addon on
  // screen until the user decides.
  const [shown, setShown] = useState(addonProp);
  const [declined, setDeclined] = useState<string | null | undefined>(undefined);
  if (addonProp !== shown && (!anyDirty || !shown)) setShown(addonProp);
  const holding = addonProp !== shown && anyDirty && !!shown;
  useEffect(() => {
    if (!holding) {
      setDeclined(undefined);
      return;
    }
    if (declined === addonProp) return;
    const held = shown;
    guard(
      addonProp ? "switching to another addon" : "closing",
      () => setShown(addonProp),
      () => {
        setDeclined(addonProp);
        if (held) onRestore?.(held);
      },
    );
  }, [holding, declined, addonProp, shown, guard, onRestore]);
  const addon = shown;
  const open = !!addon;

  const tabRef = useRef(tab);
  tabRef.current = tab;
  const guardedSetTab = (next: Tab) => {
    if (next === tab) return;
    guard("switching tabs", () => {
      setTab(next);
      onTabChange?.(next);
    });
  };
  const guardedClose = useCallback(() => {
    guard("closing", () => {
      setPanels({});
      onClose?.();
    });
  }, [guard, onClose]);
  const openPage = useCallback(() => {
    if (!onOpenPage) return;
    guard("opening the full page", () => {
      setPanels({});
      onOpenPage(tabRef.current);
    });
  }, [guard, onOpenPage]);

  // The URL is the source of truth for the tab. Panels are only reset
  // when the addon itself changes — resetting on a tab change would wipe
  // the registration the new tab just made.
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
    if (!open || !onClose) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // A dialog opened from inside (restore confirm, …) handles its own
      // Escape; don't also close the overlay under it.
      if (escapeHandledAbove(e, rootRef.current?.closest<HTMLElement>("[role=dialog]") ?? rootRef.current)) return;
      guardedClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose, guardedClose]);

  const addons = useAddons(project);
  const data = (addons.data ?? []).find((a) => a.metadata.name === addon);
  const kind = data?.spec.kind ?? "";
  const isPostgres = kind === "postgres";
  // The SQL browser works against any postgres-wire addon; backups and
  // restore stay postgres-only (the restore path is chart-specific).
  const isSQLCapable = kind === "postgres" || kind === "postgres-ha";
  const canSQL = useCanOnProject(project, Perms.SQLRead);
  const canWriteAddon = useCanOnProject(project, Perms.AddonsWrite);
  const visibleTabs = TABS.filter((t) => {
    if (t.id === "backups") return isPostgres;
    if (t.id === "sql") return isSQLCapable && canSQL;
    if (t.id === "settings") return canWriteAddon;
    return true;
  });
  useOverlayKeyboard({
    open,
    panelRef: rootRef,
    restoreKey: addon ? `addon:${addon}` : undefined,
    manageFocus: !page,
    keys: onOpenPage ? { o: openPage } : undefined,
    chords: Object.fromEntries(visibleTabs.map((t) => [t.chord as string, () => guardedSetTab(t.id)])),
  });

  // The Data browser is full-bleed on the page; the rest read better at a measure.
  const width = page ? cn("mx-auto w-full", tab === "sql" ? "h-full max-w-none" : "max-w-5xl lg:px-3 lg:py-2") : "h-full";
  const activeEntry = Object.values(panels).find((e) => e.dirty && e.onSave);

  const content = open ? (
    <div ref={rootRef} tabIndex={-1} className="flex h-full min-h-0 flex-col outline-none">
      <AddonHeader
        project={project}
        addon={addon ?? ""}
        kind={kind}
        cr={data}
        page={page}
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
        onOpenPage={onOpenPage ? openPage : undefined}
        onClose={onClose ? guardedClose : undefined}
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
          <div className={width}>
            {!data && addons.isError ? (
              <div className="p-6">
                <QueryErrorState what="addon" error={addons.error} onRetry={() => void addons.refetch()} />
              </div>
            ) : !data && addons.isSuccess ? (
              <div className="p-6">
                <p className="text-sm text-[var(--text-secondary)]">
                  No addon called <span className="font-mono">{addon}</span> in {project}.
                </p>
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
          </div>
          {/* Several forms can live on one tab; the first dirty one owns the bar. */}
          <SaveBar entry={activeEntry} />
        </AddonOverlayDirtyContext.Provider>
      </div>
    </div>
  ) : null;

  return (
    <>
      {frame ? frame({ open, label: `Addon ${addon ?? ""}`, content, requestClose: guardedClose, tab }) : content}
      {leaveDialog}
    </>
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
  onOpenPage,
  onClose,
  page,
}: {
  project: string;
  addon: string;
  kind: string;
  cr?: KusoAddon;
  onConnect: () => void;
  onOpenBackups: () => void;
  onOpenPage?: () => void;
  onClose?: () => void;
  page: boolean;
}) {
  const health = useAddonHealth(project, addon, cr);
  const version = cr?.spec.version;
  const publicPort = cr?.spec.publicTCP?.enabled ? cr.spec.publicTCP.port : undefined;
  // Same naming as the canvas and the service overlay: the short name is
  // the title, the real resource name sits underneath.
  const displayName = serviceShortName(project, addon);
  return (
    <header
      className={cn(
        "flex shrink-0 items-start gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40",
        page ? "px-4 py-4 sm:gap-3 sm:px-6 lg:px-8 lg:py-6" : "px-3 py-3 sm:gap-3 sm:px-5 sm:py-4",
      )}
    >
      <span
        className={cn(
          "mt-0.5 inline-flex shrink-0 items-center justify-center text-[var(--text-primary)]",
          page
            ? "h-11 w-11 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-sm)]"
            : "h-9 w-9 rounded-md bg-[var(--bg-tertiary)]",
        )}
      >
        <AddonIcon kind={kind} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <h2 className={cn("truncate font-heading font-semibold tracking-tight", page ? "text-2xl" : "text-lg")} title={addon}>
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
            <span>{addonVersionLabel(addonLabel(kind), version, health.runningVersion)}</span>
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
      {onOpenPage ? (
        <button
          type="button"
          onClick={onOpenPage}
          title="Open the full page (O)"
          className="hidden h-8 shrink-0 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] pl-2 pr-1.5 text-xs font-medium text-[var(--text-primary)] shadow-[var(--shadow-sm)] hover:border-[var(--border-strong)] sm:inline-flex"
        >
          <Maximize2 aria-hidden className="h-3.5 w-3.5" />
          Open full page
          <Kbd className="ml-0.5">O</Kbd>
        </button>
      ) : null}
      {onClose ? (
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      ) : null}
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
  // S3 comes first: a schedule without it still backs up nothing.
  const fix = !fixable
    ? null
    : listState.kind === "no-s3"
      ? { text: "S3 isn't set up", action: "Set up S3", href: "/settings/backups#s3-storage" }
      : !cr?.spec.backup?.schedule?.trim()
        ? { text: "No backup schedule", action: "Set one up", onClick: onOpenBackups }
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
      {/* A blocked button says why in plain sight, not only on hover. */}
      {!fix && !state.enabled && !!cr && listState.kind !== "loading" && (
        <span className="text-xs text-[var(--text-tertiary)]">{state.reason}</span>
      )}
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
