"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { ReasonTooltip } from "@/components/ui/tooltip";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import {
  useService,
  useDrift,
  useBuilds,
  rollbackBuild,
  useServiceCrons,
  useRuns,
  usePatchService,
  useRestartService,
  useWakeService,
  pickRollbackTarget,
  healthProblem,
  needsRestart,
  type HealthProblem,
} from "@/features/services";
import { useServiceErrorCount } from "@/features/errors";
import { useEnvironments } from "@/features/projects";
import { buildRefLabel } from "@/features/builds";
import { envGroupName, wakeEnvParam } from "@/lib/env-group";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Undo2, RotateCw, Sun, AlertTriangle, ScrollText, Rocket } from "lucide-react";
import type { KusoEnvironment } from "@/types/projects";
import { useCan, useCanOnProject, Perms } from "@/features/auth";
import Link from "next/link";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Skeleton } from "@/components/ui/skeleton";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { deployStatusFromServerState } from "@/components/service/DeployStatusPill";
import { ServiceDeploymentsPanel } from "./overlay/ServiceDeploymentsPanel";
import { ServiceVariablesPanel } from "./overlay/ServiceVariablesPanel";
import { ServiceMetricsPanel } from "./overlay/ServiceMetricsPanel";
import { ServiceCronsPanel } from "./overlay/ServiceCronsPanel";
import { ServiceRunsPanel } from "./overlay/ServiceRunsPanel";
import { ServiceLogsPanel } from "./overlay/ServiceLogsPanel";
import { ServiceErrorsPanel } from "./overlay/ServiceErrorsPanel";
import { ServiceVulnerabilitiesPanel } from "./overlay/ServiceVulnerabilitiesPanel";
import { ServiceSettingsPanel } from "./overlay/ServiceSettingsPanel";
import dynamic from "next/dynamic";
import { LoadingState } from "@/components/ui/loading-state";

// @xterm/xterm (+ its fit addon and stylesheet) is one of the heaviest
// deps in the bundle and only matters once the user opens the Terminal
// tab. Static import dragged it into the first-load JS of every route
// that renders ServiceOverlay (the whole project view). next/dynamic
// with ssr:false splits it into its own lazy chunk — same pattern as
// ProjectCanvas in app/(app)/projects/[project]/view.tsx (and xterm,
// like ReactFlow, touches browser globals that break the static-export
// prerender pass anyway).
const ServiceTerminalPanel = dynamic(
  () => import("./overlay/ServiceTerminalPanel").then((m) => m.ServiceTerminalPanel),
  {
    ssr: false,
    loading: () => <LoadingState kind="card" className="m-5" />,
  },
);
import { FailureBanner } from "./overlay/FailureBanner";
import { OverlayTabs, type OverlayTabItem } from "./overlay/OverlayTabs";
import { useOverlayKeyboard } from "./overlay/useOverlayKeyboard";
import { ServiceHeaderActions } from "./overlay/ServiceHeaderActions";
import { ServiceHealthStrip } from "./overlay/ServiceHealthStrip";
import { ExternalLink, X, Pencil, Maximize2, Minimize2 } from "lucide-react";
import { CopyButton } from "@/components/ui/copy-button";
import { StatusPill as SharedStatusPill } from "@/components/ui/status-pill";
import { SERVICE_STATUS, serverStateCause, type ServiceStatus } from "@/lib/status";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { cn } from "@/lib/utils";
import { escapeHandledAbove } from "@/lib/escape-layer";
import { useUnsavedEdits } from "@/lib/unsaved-edits";
import { invalidateProjectDescribe } from "@/features/projects/hooks";
import { hostScheme } from "@/lib/host-scheme";

// OverlayDirtyContext lets every panel inside ServiceOverlay register
// whether its form has unsaved edits AND (optionally) the save +
// discard handlers the shell's unified SaveBar fires. Panels that
// need their own inline button still can — useOverlayDirty without
// onSave keeps the existing dirty-tracking-only behaviour.
type PanelEntry = {
  dirty: boolean;
  onSave?: () => void | Promise<void>;
  onDiscard?: () => void;
  saving?: boolean;
  // Last save error message for this panel. When set, the unified
  // SaveBar surfaces it inline next to the Save button so users see
  // *why* a save failed without having to chase a toast that may
  // have already disappeared. Cleared by the panel on next save
  // attempt (or once the panel goes clean).
  saveError?: string;
};
type OverlayDirtyAPI = {
  setPanel: (key: string, entry: PanelEntry) => void;
  clearPanel: (key: string) => void;
};
const OverlayDirtyContext = createContext<OverlayDirtyAPI | null>(null);
export function useOverlayDirty(
  panelKey: string,
  dirty: boolean,
  opts?: {
    onSave?: () => void | Promise<void>;
    onDiscard?: () => void;
    saving?: boolean;
    saveError?: string;
  }
) {
  const api = useContext(OverlayDirtyContext);
  // Panels pass fresh closures every render. Registering those directly
  // made the effect re-run on every render, and each run sets overlay
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

type Tab =
  | "deployments"
  | "variables"
  | "metrics"
  | "logs"
  | "errors"
  | "vulnerabilities"
  | "shell"
  | "crons"
  | "runs"
  | "settings";
// Settings is pinned to the right of the strip (rendered outside
// the scrollable container) because it holds the destructive
// actions — Delete service, change runtime, change port, change
// scale. At 1280px the seven-peer-tab layout used to crop Settings
// behind the scroll edge; users had to scroll to discover the
// most-important destination. Pinning it surfaces the affordance
// at any viewport.
//
// Crons and Runs always show for users who can create them (admin —
// the server gates creation on secrets:read); for everyone else they
// only appear once the service has at least one cron / run.
// The strip keeps this order at every width (it scrolls when narrow);
// secondary tabs always sit under "More".
const ALL_MAIN_TABS: { id: Tab; label: string; chord?: string; alwaysShow?: boolean; secondary?: boolean }[] = [
  { id: "deployments", label: "Deployments", chord: "d", alwaysShow: true },
  { id: "variables", label: "Variables", chord: "v", alwaysShow: true },
  { id: "logs", label: "Logs", chord: "l", alwaysShow: true },
  { id: "metrics", label: "Metrics", chord: "m", alwaysShow: true },
  { id: "errors", label: "Errors", chord: "e", alwaysShow: true },
  { id: "shell", label: "Shell", chord: "t", alwaysShow: true },
  // Always under More, so every service type shows the same strip.
  { id: "vulnerabilities", label: "Vulnerabilities", chord: "u", alwaysShow: true, secondary: true },
  { id: "crons", label: "Crons", chord: "c", secondary: true },
  { id: "runs", label: "Runs", chord: "r", secondary: true },
];
const PINNED_TAB: { id: Tab; label: string; chord?: string } = { id: "settings", label: "Settings", chord: "s" };

// TAB_ALIASES maps deep-link tab slugs that aren't overlay tabs onto the
// tab that owns that content. The server's failures.TabBuild ("build")
// tags build-time failures (build OOM, lockfile drift, missing
// Dockerfile, …); the failed build row with its remediation lives on
// Deployments, so that's where the banner has to render.
const TAB_ALIASES: Record<string, Tab> = { build: "deployments", vulns: "vulnerabilities" };

function resolveTab(slug: string | undefined): Tab | undefined {
  if (!slug) return undefined;
  const aliased = TAB_ALIASES[slug];
  if (aliased) return aliased;
  const match = [...ALL_MAIN_TABS, PINNED_TAB].find((t) => t.id === slug);
  return match?.id;
}

// EditableTitle renders the service's display name in the overlay header
// with inline click-to-edit. Click the title (or the pencil that appears
// on hover) → it becomes an input; Enter/blur saves, Escape cancels.
// Saving PATCHes spec.displayName only (a cosmetic label — no rebuild,
// no URL/CR-name change). Clearing the field falls the title back to the
// URL slug. This is the rename affordance users reach for in the header.
function EditableTitle({
  project,
  service,
  displayName,
}: {
  project: string;
  service: string;
  displayName: string;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(displayName);
  const inputRef = useRef<HTMLInputElement>(null);
  const patch = usePatchService(project, service);
  // shown when not editing: displayName when set, else the slug.
  const shown = displayName || service;

  useEffect(() => {
    if (editing) {
      setValue(displayName);
      // focus + select after the input mounts
      requestAnimationFrame(() => inputRef.current?.select());
    }
  }, [editing, displayName]);

  const commit = useCallback(async () => {
    const trimmed = value.trim();
    // No change → just close.
    if (trimmed === displayName) {
      setEditing(false);
      return;
    }
    if (trimmed && !/^[A-Za-z0-9 -]{1,60}$/.test(trimmed)) {
      toast.error("Display name: letters / digits / spaces / hyphens, ≤60 chars");
      return;
    }
    try {
      // Empty string clears the label back to the slug.
      await patch.mutateAsync({ displayName: trimmed });
      toast.success(trimmed ? `Renamed to "${trimmed}"` : "Display name cleared");
      setEditing(false);
    } catch (e) {
      toastError(e, "Rename failed");
    }
  }, [value, displayName, patch]);

  if (editing) {
    return (
      <input
        ref={inputRef}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            void commit();
          } else if (e.key === "Escape") {
            e.preventDefault();
            setEditing(false);
          }
        }}
        maxLength={60}
        spellCheck={false}
        placeholder={service}
        aria-label="Service display name"
        className="font-heading text-lg font-semibold tracking-tight bg-transparent border-b border-[var(--accent)] outline-none min-w-0 w-[20ch] max-w-full"
      />
    );
  }

  return (
    <button
      type="button"
      onClick={() => setEditing(true)}
      title="Rename (visual label only)"
      className="group/title flex items-center gap-1.5 min-w-0 text-left"
    >
      <span className="font-heading text-lg font-semibold tracking-tight truncate">
        {shown}
      </span>
      <Pencil className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)] opacity-0 transition-opacity group-hover/title:opacity-100" />
    </button>
  );
}

interface Props {
  project: string;
  service: string | null;
  env?: string; // "production" | preview short name
  // defaultTab lets canvas right-click menus deep-link into a specific
  // tab (Logs, Errors, …). Undefined falls back to the per-service
  // default of "deployments".
  defaultTab?: string;
  // Failure deep-link signal from the bell-popover. When set, a
  // FailureBanner renders at the top of the routed tab with the
  // kind-specific copy. The kind→tab mapping is decided server-side
  // by internal/failures and arrives as the matching ?tab= param.
  // (?highlight=<n> from the same URL is parsed by view.tsx but not
  // yet plumbed into the FTS-search-based Logs tab — best-effort
  // scroll-to-line will land in a follow-up once the viewer has a
  // line-anchor surface.)
  failureKind?: string;
  // Called when the tab changes (user click, g-chord, in-panel link) and
  // once on open when no tab was requested, so the page can keep ?tab= in
  // the URL. The URL then flows back in as defaultTab.
  onTabChange?: (tab: string) => void;
  onClose: () => void;
}

// ServiceOverlay is the in-page inspector shown when a service is
// clicked on the canvas/list. The page mirrors the open service and tab
// into the URL (?service=&tab=); clicking outside or pressing ESC closes
// it. Slides in from the right with a spring; the dimmed backdrop is its
// own click target so peripheral clicks dismiss the
// panel without bubbling into canvas pan/drag.
export function ServiceOverlay({
  project,
  service,
  env: envParam = "production",
  defaultTab,
  failureKind,
  onTabChange,
  onClose,
}: Props) {
  const open = !!service;
  const [tab, setTab] = useState<Tab>("deployments");
  const panelRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useExpandedPanel();

  // Per-panel dirty + save registry. Children call useOverlayDirty
  // and (optionally) supply onSave/onDiscard so the shell can render
  // one SaveBar at the bottom for the active tab, instead of every
  // panel rolling its own button placement.
  const dirtyMap = useRef<Record<string, boolean>>({});
  const [panels, setPanels] = useState<Record<string, PanelEntry>>({});
  useUnsavedEdits("service-overlay", Object.values(panels).some((p) => p.dirty));
  const dirtyAPI = useMemo<OverlayDirtyAPI>(
    () => ({
      setPanel: (key, entry) => {
        if (entry.dirty) dirtyMap.current[key] = true;
        else delete dirtyMap.current[key];
        setPanels((prev) => ({ ...prev, [key]: entry }));
      },
      clearPanel: (key) => {
        delete dirtyMap.current[key];
        setPanels((prev) => {
          const next = { ...prev };
          delete next[key];
          return next;
        });
      },
    }),
    []
  );

  // Clicking the backdrop / X closes and drops unsaved edits; the Save
  // bar's dirty pip is the signal there. Switching tabs with unsaved
  // edits asks first: the panel unmounts, so a stray tab click would
  // otherwise lose Variables edits silently.
  const guardedClose = useCallback(() => {
    dirtyMap.current = {};
    setPanels({});
    onClose();
  }, [onClose]);

  const tabRef = useRef(tab);
  tabRef.current = tab;
  const onTabChangeRef = useRef(onTabChange);
  onTabChangeRef.current = onTabChange;
  const [pendingTab, setPendingTab] = useState<{ tab: Tab; anchor?: string } | null>(null);
  const switchTab = useCallback((next: Tab, anchor?: string) => {
    dirtyMap.current = {};
    setPanels({});
    setTab(next);
    onTabChangeRef.current?.(next);
    if (anchor) {
      window.setTimeout(() => {
        document.getElementById(anchor)?.scrollIntoView({ behavior: smoothScroll(), block: "start" });
      }, 250);
      // Cards above the target (domains, pods) can still be loading and
      // push it down after the first scroll; settle it once more.
      window.setTimeout(() => {
        document.getElementById(anchor)?.scrollIntoView({ behavior: smoothScroll(), block: "start" });
      }, 1200);
    }
  }, []);
  const guardedSetTab = useCallback(
    (next: Tab, anchor?: string) => {
      if (next !== tabRef.current && Object.keys(dirtyMap.current).length > 0) {
        setPendingTab({ tab: next, anchor });
        return;
      }
      switchTab(next, anchor);
    },
    [switchTab],
  );

  // When a service opens — or the URL's ?tab= changes under us (Back /
  // Forward) — land on the requested tab (right-click "View logs" → Logs,
  // etc.) or fall back to the user's last-used tab in this session. Falls back to Deployments — the most actionable
  // default — when there's no remembered tab. Iterating across
  // services to compare logs no longer punishes the user with a
  // forced "back to Deployments" reset on every open.
  useEffect(() => {
    if (!service) return;
    // Validate against the universe of possible tabs. Visibility of
    // Crons/Runs is enforced separately below — landing on a hidden
    // tab would mean the active-tab underline has nothing to anchor
    // to, so we coerce that case back to "deployments".
    const requested = resolveTab(defaultTab);
    if (requested) {
      setTab(requested);
      return;
    }
    let remembered: Tab = "deployments";
    try {
      const v = window.sessionStorage.getItem("kuso-service-overlay-tab");
      if (v && [...ALL_MAIN_TABS, PINNED_TAB].some((t) => t.id === v)) remembered = v as Tab;
    } catch {
      // Storage blocked: Deployments it is.
    }
    setTab(remembered);
    // Write the fallback into the URL so a reload or a shared link lands
    // on exactly this tab.
    onTabChangeRef.current?.(remembered);
  }, [service, defaultTab]);


  // Remember which tab the failure deep-link routed to, so the
  // FailureBanner only shows on that tab — switching tabs is the
  // user telling us they're done with the hint. Captured once per
  // open: re-derives from defaultTab when the user re-opens via a
  // different notification.
  const failureTab = failureKind ? resolveTab(defaultTab) : undefined;

  // Persist tab selection so the next open in this session lands on
  // the same place. SessionStorage (not localStorage) so a new tab/
  // window starts fresh.
  useEffect(() => {
    try {
      window.sessionStorage.setItem("kuso-service-overlay-tab", tab);
    } catch {
      // Storage blocked: the URL still carries the tab.
    }
  }, [tab]);

  // Escape is easy to hit by accident (and used to leak through from
  // nested dialogs), so with unsaved edits it asks first. Clicking the
  // backdrop / X still discards without asking, per the comment above.
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // Close on ESC + lock body scroll while open.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (escapeHandledAbove(e, rootRef.current)) return;
      if (Object.keys(dirtyMap.current).length > 0) {
        setConfirmDiscard(true);
        return;
      }
      guardedClose();
    };
    window.addEventListener("keydown", onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [open, guardedClose]);

  const svc = useService(project, service ?? "");
  const drift = useDrift(project, service ?? "");
  const envs = useEnvironments(project);
  // Data-driven tab visibility: Crons + Runs only show in the strip
  // when the service actually has at least one of each. The hooks
  // share their cache key with the panels (CronsPanel + RunsPanel)
  // so opening the tab doesn't double-fetch. While the count is
  // unknown (initial load, refetch) we conservatively show the tab —
  // hiding mid-session would feel like the UI is moving under the
  // user. Counts > 0 keep the tab; counts === 0 hide it.
  const crons = useServiceCrons(project, service ?? "");
  const runs = useRuns(project, service ?? "");
  const builds = useBuilds(project, service ?? "");
  // Build-time failure kinds carry the classifier's lineHint + remediation
  // on the build record; the bell link only has ?kind=. Use the newest
  // build with that kind so the banner shows the same fix BuildRow does.
  // Runtime kinds (crash_loop, oom, …) have no build record to read from.
  const failureClass = useMemo(
    () =>
      failureKind
        ? (builds.data ?? []).find((b) => b.failureClass?.kind === failureKind)?.failureClass
        : undefined,
    [builds.data, failureKind],
  );
  // Creating a service cron or run is admin-only server-side.
  const canCreateJobs = useCanOnProject(project, Perms.SecretsRead);
  const showCrons = canCreateJobs || (crons.data?.length ?? -1) !== 0;
  const showRuns = canCreateJobs || (runs.data?.length ?? -1) !== 0;
  // Shell is project-admin only on the server (terminal_ws.go gates on
  // auth.PermShellExec). Hide the tab for everyone else — same call
  // AddonOverlay makes for its SQL tab: rendering a terminal that can
  // only 403 on connect is worse than not offering it.
  const canShell = useCanOnProject(project, Perms.ShellExec);
  // Kube events live on Settings → Health, which is admin-only.
  const canSeeEvents = useCan(Perms.SettingsAdmin);
  // Always show whichever tab the user is on so the active-tab
  // underline has something to anchor to after a deep link.
  const MAIN_TABS = useMemo(
    () =>
      ALL_MAIN_TABS.filter((t) => {
        if (t.id === "shell") return canShell;
        if (t.alwaysShow) return true;
        if (t.id === "crons") return showCrons || tab === "crons";
        if (t.id === "runs") return showRuns || tab === "runs";
        return true;
      }),
    [showCrons, showRuns, tab, canShell],
  );

  // Same count the canvas badge, project card and Errors tab headline show.
  const errorCount = useServiceErrorCount(project, service ?? "").data?.occurrences ?? 0;
  const tabItems = useMemo<OverlayTabItem<Tab>[]>(
    () =>
      MAIN_TABS.map((t) =>
        t.id === "errors" && errorCount > 0 ? { ...t, badge: errorCount, badgeTone: "error" as const } : t,
      ),
    [MAIN_TABS, errorCount],
  );

  useOverlayKeyboard({
    open,
    panelRef,
    restoreKey: service ? `service:${service}` : undefined,
    chords: Object.fromEntries(
      [...MAIN_TABS, PINNED_TAB].filter((t) => t.chord).map((t) => [t.chord as string, () => guardedSetTab(t.id)]),
    ),
  });

  // If the visibility-driven tab list drops the current tab (e.g.
  // user was on Crons and deleted their last cron), fall back to
  // Deployments so the strip + content stay in sync. Only fires on
  // the true→false edge: a user who explicitly deep-linked into
  // Crons-with-zero-crons (right-click "Add cron…" on a service with
  // none) stays on the tab so they can add their first one. Settings
  // is separate (PINNED_TAB) — always visible, never falls.
  const prevShowCrons = useRef(showCrons);
  const prevShowRuns = useRef(showRuns);
  useEffect(() => {
    if (tab === "settings") return;
    if (tab === "crons" && prevShowCrons.current && !showCrons) setTab("deployments");
    if (tab === "runs" && prevShowRuns.current && !showRuns) setTab("deployments");
    // Shell hidden for non-admins — a remembered/deep-linked shell tab
    // would otherwise render with no strip entry to anchor to.
    if (tab === "shell" && !canShell) setTab("deployments");
    prevShowCrons.current = showCrons;
    prevShowRuns.current = showRuns;
  }, [tab, showCrons, showRuns, canShell]);
  const fqn = service ? project + "-" + service : "";
  // Find the env CR matching (service, envParam). The previous
  // split("-").slice(-2) heuristic produced "frontend-staging" for
  // an env CR named "tickero-frontend-staging" — comparing that
  // against envParam="staging" returned no match, so the overlay
  // fell back to the service-level domains list (production URL)
  // on every non-production tab. Use the kuso.sislelabs.com/env
  // label which is the canonical env short-name, with a single-
  // segment fallback for legacy CRs without the label.
  const env = (envs.data ?? []).find((e) => {
    if (e.spec.service !== fqn) return false;
    const envLabel = (e.metadata.labels ?? {})["kuso.sislelabs.com/env"];
    if (envParam === "production") {
      // Group identity is the label — a staging clone carries
      // spec.kind="production" too, so matching on spec.kind pulled the
      // clone into the "production" tab. Prefer the label; fall back to
      // spec.kind for legacy CRs missing it.
      if (envLabel) return envLabel === "production";
      return e.spec.kind === "production";
    }
    if (envLabel) return envLabel === envParam;
    // Fallback: last segment of the CR name (tickero-frontend-staging → staging).
    const parts = e.metadata.name.split("-");
    return parts[parts.length - 1] === envParam;
  });

  // URL fallback chain — mirrors ServiceNode.tsx so the overlay
  // header and the canvas chip agree.
  //
  // Pre-fix this read only env.status.url which the operator never
  // populates today, so every overlay header said "no URL yet" even
  // for envs with a host. The canvas already had the fallback; we
  // were just missing the same chain here. Order:
  //   internal=true        → no public URL (Service exists, no Ingress)
  //   env.additionalHosts  → the env's custom domain (v0.16.19+ per-env
  //                           custom domains; canonical public URL, so
  //                           prefer it over the auto-host). Mirrors
  //                           the canvas ServiceNode resolution.
  //   env.spec.host        → the env's primary host (auto-domain)
  //   service custom       → service-level domains[0] when env.host is empty
  //   env.status.url       → legacy fallback for old data
  const svcInternal = !!svc.data?.spec?.internal;
  const envHost = env?.spec?.host;
  const envTLS = env?.spec?.tlsEnabled ?? true;
  const envCustomHost = env?.spec?.additionalHosts?.find((h) => !!h);
  const customDomain = svc.data?.spec?.domains?.find((d) => d?.host)?.host;
  const customTLS = svc.data?.spec?.domains?.find((d) => d?.host)?.tls ?? true;
  const url = svcInternal
    ? undefined
    : envCustomHost
      ? `${hostScheme(envTLS, envCustomHost)}://${envCustomHost}`
      : envHost
        ? `${hostScheme(envTLS, envHost)}://${envHost}`
        : customDomain
          ? `${customTLS ? "https" : "http"}://${customDomain}`
          : (env?.status?.url as string | undefined);
  const ready = !!env?.status?.ready;
  const phase = (env?.status?.phase as string | undefined)?.toLowerCase();
  // "rolling" is a real running state, not a diagnostic — the
  // overlay used to pack it into the drift chip alongside three
  // other meanings ("pending changes", "restart needed",
  // "helm: <err>"), which conflated state with diagnostics. Surface
  // rolling through StatusDot; the drift chip stays purely
  // diagnostic.
  const rollingNow = !!drift.data?.rolloutPending;
  const rolloutSlow = useRolloutSlow(rollingNow, drift.data?.lastSpecMutation);
  // Server-derived unified rollup (env.status.state) wins when
  // present — it folds build + deployment + pod health into one
  // answer (crashloops and degraded replica counts are invisible to
  // the legacy phase chain below). "rolling" stays a client-side
  // overlay on top of a healthy state since drift is fetched here.
  const serverState = deployStatusFromServerState(env?.status?.state);
  const stateDetail = env?.status?.stateDetail;
  // With a server rollup the header follows it exactly, like the canvas
  // node (deriveServiceStatus); an in-flight rollout is a chip next to
  // it, not a different status.
  const replicas = env?.status?.replicas as { ready?: number; desired?: number } | undefined;
  // Live but short of replicas reads "Degraded", the same rule as the
  // health strip (ready < desired from the env's replica rollup).
  const shortOfReplicas =
    replicas?.desired !== undefined && replicas.desired > 0 && (replicas.ready ?? 0) < replicas.desired;
  const status = serverState
    ? serverState === "active" && shortOfReplicas
      ? "degraded"
      : serverState
    : phase === "building" || phase === "deploying"
      ? "building"
      : ready && rollingNow
        ? "rolling"
        : ready
          ? "active"
          : phase === "failed" || phase === "error"
            ? "failed"
            : phase === "sleeping"
              ? "sleeping"
              : "unknown";

  const envGroup = env ? envGroupName(env) : envParam;
  const stopped = !!svc.data?.spec?.stopped;
  const problem = healthProblem(env?.status?.state, status);
  const restartNeeded = needsRestart(drift.data, env?.status?.state);
  const failureBannerVisible = !!failureKind && tab === failureTab;

  // Jump to another tab, optionally scrolling a Settings section into
  // view once the tab's fade-in has mounted it.
  const navigateTo = useCallback(
    (slug: string, anchor?: string) => {
      const next = resolveTab(slug);
      if (!next) return;
      guardedSetTab(next, anchor);
    },
    [guardedSetTab],
  );

  return (
    <AnimatePresence>
      {open && (
        <div
          ref={rootRef}
          className="fixed inset-0 z-50 flex"
          role="dialog"
          aria-modal="true"
          aria-label={`Service ${svc.data?.spec.displayName?.trim() || service || ""}`}
        >
          {/* Backdrop — clickable to close. */}
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
          {/* Panel — slides from the right. `relative z-10` lifts it
              above the absolutely-positioned backdrop, otherwise the
              panel sits in normal flow *behind* the backdrop and gets
              filtered by its blur. */}
          <motion.div
            ref={panelRef}
            tabIndex={-1}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            className={cn(
              "relative z-10 ml-auto flex h-full w-full flex-col bg-[var(--bg-primary)] shadow-[var(--shadow-lg)] outline-none sm:border-l sm:border-[var(--border-subtle)]",
              !expanded && "sm:max-w-3xl",
            )}
          >
            {/* Sticky header. Tighter padding on small screens so the
                overlay's title row uses the full width of a phone. */}
            <header className="flex shrink-0 items-start gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-3 sm:gap-3 sm:px-5 sm:py-4">
              <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-primary)]">
                <RuntimeIcon runtime={svc.data?.spec.runtime} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  {/* Inline click-to-edit display name. Shows the
                      cosmetic displayName when set, else the URL slug;
                      the slug + project stay below in mono so the real
                      CR name/URL are always discoverable. Editing PATCHes
                      spec.displayName only — visual, no rebuild. */}
                  {service ? (
                    <EditableTitle
                      project={project}
                      service={service}
                      displayName={svc.data?.spec.displayName?.trim() || ""}
                    />
                  ) : (
                    <h2 className="font-heading text-lg font-semibold tracking-tight truncate">
                      {service || ""}
                    </h2>
                  )}
                  <StatusPill status={status} detail={stateDetail || serverStateCause(env?.status?.state)} />
                </div>
                <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--text-secondary)]">
                  <span className="min-w-0 truncate">
                    {project}
                    <span aria-hidden className="px-1 text-[var(--text-tertiary)]">/</span>
                    <span className="font-mono">{service}</span>
                    {envParam !== "production" && (
                      <span className="ml-1.5 rounded border border-[var(--border-subtle)] px-1.5 py-px text-xs">{envParam}</span>
                    )}
                  </span>
                  {url ? (
                    <UrlPill url={url} />
                  ) : (
                    <span className="text-[var(--text-tertiary)]">No public URL yet</span>
                  )}
                </div>
                {service ? (
                  <ServiceHealthStrip project={project} service={service} env={env} envGroup={envGroup} status={SERVICE_STATUS[OVERLAY_STATUS[status] ?? "unknown"]} onNavigate={navigateTo} />
                ) : null}
                <div className="mt-2.5 flex flex-wrap items-center gap-1.5 empty:hidden">
                  {/* Diagnostic chip. The "rolling out" state lives
                      on StatusDot now — this chip exists purely for
                      actionable diagnostics:
                        - helmError: helm-operator failed; user
                          needs to check the spec.
                        - specPending: service spec ↔ env CR mismatch
                          (propagation bug; shouldn't appear in
                          steady state).
                        - podsStale w/o rollout on a healthy env: pods
                          run old config and nothing will roll them;
                          a restart applies it. (A stuck rollout also
                          leaves pods stale — the health banner owns
                          that case, since a restart won't fix it.) */}
                  {(() => {
                    const d = drift.data;
                    if (!d) return null;
                    if (d.helmError && d.helmError.length > 0) {
                      return (
                        <span
                          className="inline-flex items-center gap-1 rounded-md border border-[var(--error)]/40 bg-[var(--error-subtle)] px-2 py-0.5 text-xs text-[var(--error)]"
                          title={d.helmError}
                        >
                          Config couldn&apos;t be applied
                        </span>
                      );
                    }
                    const stale = d.podsStale && d.podsStale.length > 0;
                    const rolling = d.rolloutPending;
                    const specOff = d.specPending && d.specPending.length > 0;
                    // During an active rollout, surface an explicit
                    // "applying changes…" chip instead of hiding the
                    // header entirely. After an env-var edit the pods
                    // roll for a few seconds; a blank header made users
                    // think nothing happened (or that the stale "pending
                    // changes" badge was their save not taking). Naming
                    // the in-flight field (envVars/image/…) makes it
                    // obvious the edit was accepted and is propagating.
                    if (rolling && rolloutSlow) {
                      return (
                        <span className="inline-flex items-center gap-1.5 rounded-md border border-[var(--warning)]/40 bg-[var(--warning-subtle)] px-2 py-0.5 text-xs text-[var(--warning)]">
                          <AlertTriangle aria-hidden className="h-3 w-3" />
                          Rollout taking longer than expected
                          <span aria-hidden>·</span>
                          {canSeeEvents ? (
                            <Link href="/settings/health#kube-events" className="underline underline-offset-2">
                              View events
                            </Link>
                          ) : (
                            <button type="button" onClick={() => navigateTo("logs")} className="underline underline-offset-2">
                              View logs
                            </button>
                          )}
                        </span>
                      );
                    }
                    if (rolling) {
                      const fields = stale ? d.podsStale : [];
                      const what =
                        fields.length > 0 ? ` (${fields.join(", ")})` : "";
                      return (
                        <span
                          className="inline-flex items-center gap-1.5 rounded-md border border-[var(--info)]/40 bg-[var(--info-subtle)] px-2 py-0.5 text-xs text-[var(--info)]"
                          title={`Rolling out the latest spec${what}. Pods are restarting to pick up your change.`}
                        >
                          <span className="size-1.5 animate-pulse rounded-full bg-[var(--info)]" />
                          Applying changes{what}…
                        </span>
                      );
                    }
                    // Nothing has ever run, so there's nothing for a change to be pending against.
                    if (specOff && env?.status?.state !== "no_image") {
                      return (
                        <span
                          className="inline-flex items-center gap-1 rounded-md border border-[var(--info)]/40 bg-[var(--info-subtle)] px-2 py-0.5 text-xs text-[var(--info)]"
                          title={`Spec out of sync on: ${d.specPending.join(", ")}`}
                        >
                          Pending changes
                        </span>
                      );
                    }
                    if (stale && restartNeeded && service) {
                      return (
                        <RestartControl
                          project={project}
                          service={service}
                          envGroup={envGroup}
                          variant="chip"
                          title={`Pods still run the old ${d.podsStale.join(", ")}`}
                        />
                      );
                    }
                    return null;
                  })()}
                </div>
                {/* Actions keep one row; what doesn't fit lives in ⋯. */}
                <div className="mt-2 flex flex-nowrap items-center gap-1.5 empty:hidden">
                  {service && status === "sleeping" && !stopped ? (
                    <WakeControl project={project} service={service} env={wakeEnvParam(env, envGroup)} />
                  ) : null}
                  {service ? (
                    <ServiceHeaderActions
                      project={project}
                      service={service}
                      env={env}
                      stopped={stopped}
                      showRestart={!!env && !restartNeeded && status !== "sleeping" && problem !== "runtime"}
                    />
                  ) : null}
                </div>
              </div>
              <button
                type="button"
                onClick={() => setExpanded(!expanded)}
                aria-pressed={expanded}
                aria-label={expanded ? "Narrow the panel" : "Expand the panel to full width"}
                title={expanded ? "Narrow the panel" : "Expand to full width"}
                className="hidden h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] sm:inline-flex"
              >
                {expanded ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
              </button>
              <button
                type="button"
                onClick={guardedClose}
                aria-label="Close"
                className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-4 w-4" />
              </button>
            </header>

            {service && problem && !failureBannerVisible ? (
              <HealthBanner
                project={project}
                service={service}
                problem={problem}
                serverState={env?.status?.state}
                detail={stateDetail}
                env={env}
                envGroup={envGroup}
                onDeployments={tab === "deployments"}
                onNavigate={navigateTo}
              />
            ) : null}

            <OverlayTabs
              tabs={tabItems}
              pinned={{ ...PINNED_TAB }}
              active={tab}
              onChange={guardedSetTab}
              idPrefix="svc-overlay"
              label="Service sections"
            />

            {/* Body — switches by tab. Wraps in motion.div so each
                tab swap fades, but the body itself owns its own scroll.
                Relative for the unified SaveBar's absolute anchor. */}
            <div
              id="svc-overlay-panel"
              role="tabpanel"
              aria-labelledby={`svc-overlay-tab-${tab}`}
              className="relative flex-1 min-h-0 overflow-hidden"
            >
              {svc.isPending ? (
                <div className="space-y-3 p-6">
                  <Skeleton className="h-8 w-48" />
                  <Skeleton className="h-32 w-full" />
                  <Skeleton className="h-32 w-full" />
                </div>
              ) : svc.isError ? (
                <p className="p-6 text-sm text-[var(--error)]">
                  Failed to load service: {svc.error?.message}
                </p>
              ) : (
                <OverlayDirtyContext.Provider value={dirtyAPI}>
                <AnimatePresence mode="wait">
                  <motion.div
                    key={tab}
                    initial={{ opacity: 0, y: 4 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={{ opacity: 0, y: -4 }}
                    transition={{ duration: 0.12 }}
                    className="h-full overflow-y-auto"
                  >
                    {/* FailureBanner: rendered above the routed-tab
                        content when the bell-popover deep-linked us
                        into a classified failure AND the user is on
                        the tab the classifier picked. Switching tabs
                        is treated as dismissal — the user has moved
                        on from the hint. */}
                    {failureBannerVisible && (
                      <div className="px-5 pt-5">
                        <FailureBanner
                          kind={failureKind}
                          lineHint={failureClass?.lineHint}
                          remediation={failureClass?.remediation}
                          onNavigate={navigateTo}
                        />
                      </div>
                    )}
                    {/* First-deploy coachmark — fires on the user's
                        very first green build (1 succeeded build,
                        finished within the last 60s) and only on the
                        Deployments tab (where they'll naturally land
                        after a deploy). Self-gates on localStorage so
                        re-opens after dismissal stay quiet. */}
                    {tab === "deployments" && (
                      <div className="p-5">
                        <ServiceDeploymentsPanel project={project} service={service ?? ""} env={env} url={url} onNavigate={navigateTo} />
                      </div>
                    )}
                    {tab === "variables" && (
                      <div className="p-5">
                        <ServiceVariablesPanel
                          project={project}
                          service={service ?? ""}
                          // Resolve env-group short name from the env CR's
                          // canonical label (production / staging /
                          // preview-pr-N / custom). Fall back to "production"
                          // when no env is selected yet so the panel still
                          // has a valid scope to query per-env secrets
                          // against. Mirrors ServiceDeploymentsPanel which
                          // derives env scope from the same label.
                          env={
                            env?.metadata?.labels?.["kuso.sislelabs.com/env"] ??
                            env?.spec?.kind ??
                            "production"
                          }
                        />
                      </div>
                    )}
                    {tab === "metrics" && (
                      <div className="p-5">
                        {/* env: the resolved env CR for the active
                            env-switcher selection, so the panel reads
                            THIS env's pods/timeseries — previously it
                            always looked up production itself. */}
                        <ServiceMetricsPanel
                          project={project}
                          service={service ?? ""}
                          env={env}
                          memoryLimit={svc.data?.spec.resources?.limits?.memory}
                          onViewLogs={() => guardedSetTab("logs")}
                        />
                      </div>
                    )}
                    {tab === "logs" && (
                      <div className="p-5">
                        <ServiceLogsPanel
                          key={env?.metadata.name ?? "none"}
                          project={project}
                          service={service ?? ""}
                          onOpenDeployments={() => guardedSetTab("deployments")}
                          defaultEnv={env ? envGroupName(env) : ""}
                          streamEnv={env?.metadata.name}
                        />
                      </div>
                    )}
                    {tab === "errors" && (
                      <div className="p-5">
                        <ServiceErrorsPanel
                          project={project}
                          service={service ?? ""}
                          onViewLogs={() => guardedSetTab("logs")}
                        />
                      </div>
                    )}
                    {tab === "vulnerabilities" && (
                      <div className="p-5">
                        <ServiceVulnerabilitiesPanel
                          key={`${service ?? ""}/${env ? envGroupName(env) : envParam}`}
                          project={project}
                          service={service ?? ""}
                          env={env ? envGroupName(env) : envParam}
                        />
                      </div>
                    )}
                    {tab === "shell" && (
                      <div className="p-5">
                        <ServiceTerminalPanel
                          project={project}
                          service={service ?? ""}
                          env={envParam}
                          blocked={stopped ? "stopped" : status === "sleeping" ? "asleep" : undefined}
                          wakeEnv={wakeEnvParam(env, envGroup)}
                        />
                      </div>
                    )}
                    {tab === "crons" && (
                      <div className="p-5">
                        <ServiceCronsPanel
                          project={project}
                          service={service ?? ""}
                          // Deep link (canvas "Add cron…") into an empty
                          // list opens the create form straight away.
                          defaultAdding={defaultTab === "crons" && crons.data?.length === 0}
                        />
                      </div>
                    )}
                    {tab === "runs" && (
                      <div className="p-5">
                        <ServiceRunsPanel project={project} service={service ?? ""} />
                      </div>
                    )}
                    {tab === "settings" && (
                      // ServiceSettingsPanel handles its own padding
                      // (sticky sidebar nav + sectioned form). Wrapping
                      // in p-5 here would double-pad and break the
                      // sticky-positioning math.
                      // Keyed on identity so ⌘K-switching to another
                      // service can't carry this form's edits onto it.
                      <ServiceSettingsPanel
                        key={`${project}/${service ?? ""}/${envParam ?? ""}`}
                        project={project}
                        service={service ?? ""}
                        svc={svc.data}
                        env={envParam}
                      />
                    )}
                  </motion.div>
                </AnimatePresence>
                {/* Unified SaveBar — renders for the active tab when
                    its panel has registered an onSave via
                    useOverlayDirty. Sits above the panel scroll so
                    Save is one click regardless of how far the user
                    has scrolled inside a long form. Panels that
                    don't register onSave (Deployments, Logs, etc)
                    skip this entirely. */}
                {(() => {
                  const active = panels[tab];
                  if (!active || !active.dirty || !active.onSave) return null;
                  return (
                    <div className="pointer-events-none absolute inset-x-0 bottom-3 z-30 flex justify-center px-3">
                      <div className="pointer-events-auto flex max-w-[90%] flex-col gap-1 rounded-md border border-[var(--border-strong)] bg-[var(--bg-elevated)] px-3 py-2 shadow-[var(--shadow-lg)]">
                        <div className="flex items-center gap-3">
                          <span className="text-xs text-[var(--text-secondary)]">
                            Unsaved changes
                          </span>
                          {active.onDiscard && (
                            <button
                              type="button"
                              onClick={() => active.onDiscard?.()}
                              disabled={active.saving}
                              className="text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] disabled:opacity-50"
                            >
                              Discard
                            </button>
                          )}
                          <button
                            type="button"
                            onClick={() => {
                              // Wrap in try/catch so a panel's onSave
                              // that throws synchronously (or returns
                              // a rejecting Promise without internal
                              // .catch) doesn't bubble up into React's
                              // event handler and leave the SaveBar
                              // stuck. The panel itself owns
                              // saveError surfacing.
                              try {
                                const r = active.onSave?.();
                                if (r && typeof (r as Promise<unknown>).then === "function") {
                                  (r as Promise<unknown>).catch(() => {});
                                }
                              } catch {
                                // already surfaced by the panel
                              }
                            }}
                            disabled={active.saving}
                            className="inline-flex h-7 items-center rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] hover:bg-[var(--btn-primary-bg-hover)] disabled:opacity-60"
                          >
                            {active.saving ? "Saving…" : "Save"}
                          </button>
                        </div>
                        {active.saveError && (
                          // Sticky inline error: a toast disappears in
                          // 4s, leaving the user with a dirty form and
                          // no clue why the previous save failed. The
                          // panel clears this on next save attempt.
                          <p
                            role="alert"
                            className="max-w-[40ch] truncate text-xs text-[var(--error)]"
                            title={active.saveError}
                          >
                            ✗ {active.saveError}
                          </p>
                        )}
                      </div>
                    </div>
                  );
                })()}
                </OverlayDirtyContext.Provider>
              )}
            </div>
          </motion.div>
          <ConfirmDialog
            open={confirmDiscard}
            title="Discard unsaved changes?"
            body="This panel has edits that haven't been saved. Closing drops them."
            confirmLabel="Discard"
            onCancel={() => setConfirmDiscard(false)}
            onConfirm={() => {
              setConfirmDiscard(false);
              guardedClose();
            }}
          />
          <ConfirmDialog
            open={!!pendingTab}
            title="Discard unsaved changes?"
            body={`Your edits in ${
              Object.keys(panels)
                .filter((k) => panels[k]?.dirty)
                .map((k) => k.charAt(0).toUpperCase() + k.slice(1))
                .join(", ") || "this tab"
            } haven't been saved. Switching tabs drops them.`}
            confirmLabel="Discard and switch"
            onCancel={() => setPendingTab(null)}
            onConfirm={() => {
              const p = pendingTab;
              setPendingTab(null);
              if (p) switchTab(p.tab, p.anchor);
            }}
          />
        </div>
      )}
    </AnimatePresence>
  );
}

// HealthBanner is the always-visible strip under the header while the
// selected env is unhealthy. Runtime problems (crashloop / degraded)
// get Logs + Roll back + Restart. Deploy problems (build or release
// hook failed) keep the last green version serving, so the only useful
// jump is to the failed build on Deployments.
const PROBLEM_LABEL: Record<string, string> = {
  crashlooping: "Crashlooping",
  degraded: "Degraded",
  build_failed: "Build failed",
  release_failed: "Release failed",
};

function HealthBanner({
  project,
  service,
  problem,
  serverState,
  detail,
  env,
  envGroup,
  onDeployments,
  onNavigate,
}: {
  project: string;
  service: string;
  problem: Exclude<HealthProblem, null>;
  // Already on Deployments: the failed build is right below, so the
  // jump button would go nowhere.
  onDeployments: boolean;
  serverState?: string;
  detail?: string;
  env?: KusoEnvironment;
  envGroup: string;
  onNavigate: (tab: string) => void;
}) {
  const label = (serverState && PROBLEM_LABEL[serverState]) || "Failed";
  const runtime = problem === "runtime";
  return (
    <div
      role="alert"
      className={cn(
        "flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2 sm:px-5",
        runtime
          ? "border-[var(--error)]/30 bg-[var(--error)]/5"
          : "border-[var(--warning)]/30 bg-[var(--warning)]/5",
      )}
    >
      <AlertTriangle
        className={cn("h-3.5 w-3.5 shrink-0", runtime ? "text-[var(--error)]" : "text-[var(--warning)]")}
      />
      <span className="text-xs font-medium text-[var(--text-primary)]">{label}</span>
      {detail ? (
        <span className="min-w-0 flex-1 truncate text-xs text-[var(--text-secondary)]" title={detail}>
          {detail}
        </span>
      ) : (
        <span className="flex-1" />
      )}
      <div className="flex shrink-0 items-center gap-1.5">
        {runtime ? (
          <>
            <BannerButton onClick={() => onNavigate("logs")} icon={ScrollText}>
              Logs
            </BannerButton>
            <RollbackControl project={project} service={service} env={env} envGroup={envGroup} />
            <RestartControl project={project} service={service} envGroup={envGroup} variant="button" />
          </>
        ) : onDeployments ? null : (
          <BannerButton onClick={() => onNavigate("deployments")} icon={Rocket}>
            Deployments
          </BannerButton>
        )}
      </div>
    </div>
  );
}

const headerButtonClass =
  "inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] h-7 px-2.5 text-xs font-medium text-[var(--text-secondary)] hover:border-[var(--border-strong)] hover:text-[var(--text-primary)] disabled:opacity-50 disabled:hover:brightness-100 aria-disabled:cursor-not-allowed aria-disabled:opacity-50";

function BannerButton({
  onClick,
  icon: Icon,
  children,
}: {
  onClick: () => void;
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
}) {
  return (
    <button type="button" onClick={onClick} className={headerButtonClass}>
      <Icon className="h-3 w-3" />
      {children}
    </button>
  );
}

// RestartControl rolls the env's pods in place (same image). "chip" is
// the amber "Restart to apply" diagnostic; "button" the plain action.
function RestartControl({
  project,
  service,
  envGroup,
  variant,
  title,
}: {
  project: string;
  service: string;
  envGroup: string;
  variant: "chip" | "button";
  title?: string;
}) {
  const [confirming, setConfirming] = useState(false);
  const restart = useRestartService(project, service);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);

  const onConfirm = async () => {
    try {
      await restart.mutateAsync(envGroup);
      toast.success(`Restarting ${service} (${envGroup})`);
      setConfirming(false);
    } catch (e) {
      toastError(e, "Restart failed");
    }
  };

  return (
    <>
      <ReasonTooltip reason={canWrite ? undefined : "Requires editor access on this project"}>
        <button
          type="button"
          disabled={restart.isPending}
          aria-disabled={!canWrite || undefined}
          onClick={() => {
            if (canWrite) setConfirming(true);
          }}
          title={canWrite ? (title ?? `Restart the ${envGroup} pods`) : undefined}
          className={
            variant === "chip"
              ? "inline-flex items-center gap-1 rounded-md border border-[var(--info)]/45 bg-[var(--info-subtle)] h-7 px-2.5 text-xs font-medium text-[var(--info)] hover:brightness-110 disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
              : headerButtonClass
          }
        >
          <RotateCw className={cn("h-3 w-3", restart.isPending && "animate-spin")} />
          {variant === "chip" ? "Restart to apply" : "Restart"}
        </button>
      </ReasonTooltip>
      <ConfirmDialog
        open={confirming}
        title={`Restart ${service}`}
        body={`Replaces the ${envGroup} pods with fresh ones running the same version.`}
        confirmLabel="Restart"
        destructive={false}
        pending={restart.isPending}
        onConfirm={onConfirm}
        onCancel={() => setConfirming(false)}
      />
    </>
  );
}

// WakeControl scales a sleeping env back up now instead of waiting for
// the next request to cold-start it. env undefined = production.
function WakeControl({ project, service, env }: { project: string; service: string; env?: string }) {
  const wake = useWakeService(project, service);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const onWake = async () => {
    try {
      await wake.mutateAsync(env);
      toast.success(`Waking ${service}`);
    } catch (e) {
      toastError(e, "Wake failed");
    }
  };
  return (
    <ReasonTooltip reason={canWrite ? undefined : "Requires editor access on this project"}>
      <button
        type="button"
        disabled={wake.isPending}
        aria-disabled={!canWrite || undefined}
        onClick={() => {
          if (canWrite) void onWake();
        }}
        title={canWrite ? "Start a pod now instead of on the next request" : undefined}
        className="inline-flex items-center gap-1 rounded-md border border-[var(--success)]/45 bg-[var(--success-subtle)] h-7 px-2.5 text-xs font-medium text-[var(--success)] hover:brightness-110 disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
      >
        <Sun className="h-3 w-3" />
        {wake.isPending ? "Waking…" : "Wake"}
      </button>
    </ReasonTooltip>
  );
}

// RollbackControl points the selected env back at the newest succeeded
// build on its branch that is older than what's live. Hidden when no
// such build exists (the server rejects cross-branch rollbacks).
function RollbackControl({
  project,
  service,
  env,
  envGroup,
}: {
  project: string;
  service: string;
  env?: KusoEnvironment;
  envGroup: string;
}) {
  const builds = useBuilds(project, service);
  const [confirming, setConfirming] = useState(false);
  const qc = useQueryClient();
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const target = useMemo(
    () =>
      pickRollbackTarget(builds.data ?? [], {
        envGroup,
        branch: env?.spec.branch,
      }),
    [builds.data, envGroup, env?.spec.branch],
  );
  const m = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (buildId: string) => rollbackBuild(project, service, buildId, envGroup),
    onSuccess: () => {
      toast.success(`Rolled ${envGroup} back to ${(target && buildRefLabel(target, 7)) || target?.id || "previous build"}`);
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "builds"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      invalidateProjectDescribe(qc, project);
      setConfirming(false);
    },
    onError: (e) => {
      toastError(e, "Rollback failed");
      setConfirming(false);
    },
  });
  if (!target) return null;
  const sha = buildRefLabel(target, 7) || target.id.slice(0, 8);
  const ageStamp = target.finishedAt ? relativeAge(target.finishedAt) : "";
  return (
    <>
      <button
        type="button"
        disabled={!canWrite || m.isPending}
        onClick={() => setConfirming(true)}
        title={
          canWrite
            ? `Roll ${envGroup} back to ${sha}${ageStamp ? ` (${ageStamp})` : ""}`
            : "Requires editor access on this project"
        }
        className="inline-flex items-center gap-1 rounded-md border border-[var(--warning)]/45 bg-[var(--warning-subtle)] h-7 px-2.5 text-xs font-medium text-[var(--warning)] hover:brightness-110 disabled:opacity-50 disabled:hover:brightness-100"
      >
        <Undo2 className="h-3 w-3" />
        Roll back
      </button>
      <ConfirmDialog
        open={confirming}
        title={`Roll ${envGroup} back to ${sha}`}
        body={`${envGroup} will run build ${sha}${ageStamp ? ` (${ageStamp})` : ""}. Redeploy to roll forward again.`}
        confirmLabel="Roll back"
        destructive={false}
        pending={m.isPending}
        onConfirm={() => m.mutate(target.id)}
        onCancel={() => setConfirming(false)}
      />
    </>
  );
}

// relativeAge formats an ISO timestamp as a compact "Nm/Nh/Nd ago"
// suffix for header chips. Falls back to the empty string on parse
// failure so the chip degrades cleanly to "rollback to <sha>" with
// no time hint.
function relativeAge(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const secs = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

// The overlay's local status keys ("active", "rolling", …) predate the
// shared vocabulary; this maps them onto it so the header says exactly
// what the canvas and the Deployments card say.
const OVERLAY_STATUS: Record<string, ServiceStatus> = {
  active: "live",
  rolling: "deploying",
  building: "building",
  deploying: "deploying",
  failed: "failing",
  crashed: "failing",
  degraded: "degraded",
  awaiting: "not_deployed",
  stopped: "stopped",
  sleeping: "sleeping",
  unknown: "unknown",
};

function StatusPill({ status, detail }: { status: string; detail?: string }) {
  const meta = SERVICE_STATUS[OVERLAY_STATUS[status] ?? "unknown"];
  return <SharedStatusPill status={meta} detail={detail || (status === "rolling" ? "Rolling out a new spec" : undefined)} />;
}

function UrlPill({ url }: { url: string }) {
  const display = url.replace(/^https?:\/\//, "");
  return (
    <span className="inline-flex min-w-0 max-w-full items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] py-0.5 pl-2 pr-0.5">
      <a
        href={url}
        target="_blank"
        rel="noreferrer"
        className="inline-flex min-w-0 items-center gap-1 font-mono text-xs text-[var(--text-primary)] hover:underline"
      >
        <span className="truncate">{display}</span>
        <ExternalLink aria-hidden className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
      </a>
      <CopyButton value={url} label="Copy URL" size="xs" />
    </span>
  );
}

const ROLLOUT_SLOW_MS = 10 * 60_000;

// useRolloutSlow flags a rollout still pending ROLLOUT_SLOW_MS after the
// spec write that started it (or, on servers that don't report that
// write, after this panel first saw it pending), so a rollout that never
// finishes stops reading as "Applying changes…" forever.
function useRolloutSlow(rolling: boolean, specWrittenAt?: string): boolean {
  const [seenAt, setSeenAt] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    setSeenAt(rolling ? Date.now() : null);
  }, [rolling]);
  const written = specWrittenAt ? Date.parse(specWrittenAt) : NaN;
  const since = Number.isNaN(written) ? seenAt : written;
  useEffect(() => {
    if (!rolling || since === null) return;
    const left = since + ROLLOUT_SLOW_MS - Date.now();
    if (left <= 0) return;
    const t = setTimeout(() => setNow(Date.now()), left + 50);
    return () => clearTimeout(t);
  }, [rolling, since]);
  return rolling && since !== null && now - since >= ROLLOUT_SLOW_MS;
}

const EXPANDED_KEY = "kuso:service-panel-expanded";

// The full-width choice is a per-viewer convenience, so it lives in
// localStorage (which may be unavailable: then it just isn't remembered).
function useExpandedPanel(): [boolean, (v: boolean) => void] {
  const [expanded, setExpandedState] = useState(false);
  useEffect(() => {
    try {
      setExpandedState(window.localStorage.getItem(EXPANDED_KEY) === "1");
    } catch {
      // storage blocked
    }
  }, []);
  const setExpanded = useCallback((v: boolean) => {
    setExpandedState(v);
    try {
      window.localStorage.setItem(EXPANDED_KEY, v ? "1" : "0");
    } catch {
      // storage blocked
    }
  }, []);
  return [expanded, setExpanded];
}
