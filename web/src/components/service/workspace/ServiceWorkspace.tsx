"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { ReasonTooltip } from "@/components/ui/tooltip";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  deriveServiceStatus,
  type HealthProblem,
  type NodeStatusKind,
} from "@/features/services";
import { useServiceErrorCount } from "@/features/errors";
import { useLiveServiceUrl } from "@/features/projects/liveDomain";
import { useEnvironments } from "@/features/projects";
import { buildRefLabel } from "@/features/builds";
import { envGroupName, wakeEnvParam } from "@/lib/env-group";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Undo2, RotateCw, Sun, AlertTriangle, ScrollText, Rocket } from "lucide-react";
import type { KusoEnvironment } from "@/types/projects";
import { useCanOnProject, Perms } from "@/features/auth";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Kbd } from "@/components/ui/kbd";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { ServiceDeploymentsPanel } from "../overlay/ServiceDeploymentsPanel";
import { ServiceVariablesPanel } from "../overlay/ServiceVariablesPanel";
import { ServiceMetricsPanel } from "../overlay/ServiceMetricsPanel";
import { ServiceCronsPanel } from "../overlay/ServiceCronsPanel";
import { ServiceRunsPanel } from "../overlay/ServiceRunsPanel";
import { ServiceLogsPanel } from "../overlay/ServiceLogsPanel";
import { ServiceErrorsPanel } from "../overlay/ServiceErrorsPanel";
import { ServiceVulnerabilitiesPanel } from "../overlay/ServiceVulnerabilitiesPanel";
import { ServiceSettingsPanel } from "../overlay/ServiceSettingsPanel";
import dynamic from "next/dynamic";
import { LoadingState } from "@/components/ui/loading-state";

// @xterm/xterm is one of the heaviest deps in the bundle and only matters
// once the user opens the Shell tab, so it gets its own lazy chunk (and,
// like ReactFlow, it touches browser globals the static prerender lacks).
const ServiceTerminalPanel = dynamic(
  () => import("../overlay/ServiceTerminalPanel").then((m) => m.ServiceTerminalPanel),
  {
    ssr: false,
    loading: () => <LoadingState kind="card" className="m-5" />,
  },
);
import { FailureBanner } from "../overlay/FailureBanner";
import { OverlayTabs, type OverlayTabItem } from "../overlay/OverlayTabs";
import { useOverlayKeyboard } from "../overlay/useOverlayKeyboard";
import { ServiceHeaderActions } from "../overlay/ServiceHeaderActions";
import { ServiceHealthStrip } from "../overlay/ServiceHealthStrip";
import { trimRepeatedCause } from "../overlay/statusCopy";
import { ExternalLink, X, Pencil, Maximize2 } from "lucide-react";
import { CopyButton } from "@/components/ui/copy-button";
import { StatusPill as SharedStatusPill } from "@/components/ui/status-pill";
import { SERVICE_STATUS, serverStateCause, stuckFacts, type ServiceStatus } from "@/lib/status";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { cn } from "@/lib/utils";
import { escapeHandledAbove } from "@/lib/escape-layer";
import { useUnsavedEdits } from "@/lib/unsaved-edits";
import { firstRunState } from "../overlay/firstRun";
import { FirstRunProvider } from "../overlay/firstRunContext";
import { useLeaveGuard, dirtyPanelKeys, type DirtyPanel } from "../overlay/leaveGuard";
import { invalidateProjectDescribe } from "@/features/projects/hooks";
import { hostScheme } from "@/lib/host-scheme";
import { OverlayDirtyContext, type OverlayDirtyAPI } from "./dirtyContext";
import { SaveBar } from "./SaveBar";
import { classifyError } from "@/lib/error-kind";
import { EmptyState } from "@/components/shared/EmptyState";
import Link from "next/link";
import { projectCanvasHref } from "./hrefs";
import { PackageX } from "lucide-react";

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
  size = "md",
}: {
  project: string;
  service: string;
  displayName: string;
  size?: "md" | "lg";
}) {
  const text = size === "lg" ? "text-2xl" : "text-lg";
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
        className={cn("font-heading font-semibold tracking-tight bg-transparent", text, " border-b border-[var(--accent)] outline-none min-w-0 w-[20ch] max-w-full")}
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
      <span className={cn("font-heading font-semibold tracking-tight truncate", text)}>
        {shown}
      </span>
      <Pencil className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)] opacity-0 transition-opacity group-hover/title:opacity-100" />
    </button>
  );
}

export type WorkspaceVariant = "peek" | "page";

// What a shell (the side-panel peek or the full page) gets to wrap the
// workspace in. `open` stays true while a dirty form holds a closed or
// switched resource on screen, so the shell must not unmount early.
export interface WorkspaceFrameArgs {
  open: boolean;
  label: string;
  content: React.ReactNode;
  // Guarded close (asks about unsaved edits first). Peek backdrop uses it.
  requestClose: () => void;
  tab: string;
}

interface Props {
  variant: WorkspaceVariant;
  project: string;
  service: string | null;
  env?: string; // "production" | env-group short name
  // Requested tab (?tab=). Undefined falls back to the last-used tab.
  defaultTab?: string;
  // Failure deep-link (?kind=) from the bell: a FailureBanner renders on
  // the routed tab until the user switches tabs.
  failureKind?: string;
  // Called on every tab change (and once on open when no tab was
  // requested) so the shell can keep ?tab= in the URL.
  onTabChange?: (tab: string) => void;
  // Puts the held resource back in the URL after "Keep editing" on a
  // switch, so the address bar matches what's shown.
  onRestore?: (service: string, env?: string) => void;
  // Peek only: closing (X, Escape, backdrop) and "Open full page".
  onClose?: () => void;
  onOpenPage?: (tab: string) => void;
  frame?: (args: WorkspaceFrameArgs) => React.ReactNode;
}

// Per-tab content width on the full page. Logs and settings use the room;
// reading-heavy tabs stay at a comfortable measure.
const PAGE_WIDTH: Partial<Record<Tab, string>> = {
  logs: "max-w-none",
  settings: "max-w-6xl",
  metrics: "max-w-6xl",
  errors: "max-w-6xl",
  shell: "max-w-none",
};

// ServiceWorkspace is everything about one service: header, health
// strip, tabs and panels, the unified SaveBar and the leave guard. It is
// rendered by two shells: ServiceOverlay (a "peek" side panel over the
// canvas) and the full page at /projects/<p>/services/<s>. All state that
// matters lives in the URL, so moving between the two keeps the tab.
export function ServiceWorkspace({
  variant,
  project,
  service: serviceProp,
  env: envParamProp = "production",
  defaultTab,
  failureKind,
  onTabChange,
  onRestore,
  onClose,
  onOpenPage,
  frame,
}: Props) {
  const page = variant === "page";
  const [tab, setTab] = useState<Tab>("deployments");
  const rootRef = useRef<HTMLDivElement>(null);

  const [panels, setPanels] = useState<Record<string, DirtyPanel>>({});
  const anyDirty = dirtyPanelKeys(panels).length > 0;
  useUnsavedEdits(page ? "service-page" : "service-overlay", anyDirty);
  const dirtyAPI = useMemo<OverlayDirtyAPI>(
    () => ({
      setPanel: (key, entry) => setPanels((prev) => ({ ...prev, [key]: entry })),
      clearPanel: (key) =>
        setPanels((prev) => {
          const next = { ...prev };
          delete next[key];
          return next;
        }),
    }),
    []
  );
  const clearPanels = useCallback(() => setPanels({}), []);
  const leave = useLeaveGuard(panels, clearPanels);

  // Resource switch (⌘K to another service, the page rail, Back/Forward)
  // arrives as a new prop. While a form is dirty the workspace keeps
  // showing the service the edits belong to and asks first; Keep editing
  // points the URL back.
  const requested = { service: serviceProp, env: envParamProp };
  const [shownRes, setShownRes] = useState(requested);
  const [declined, setDeclined] = useState<string | null>(null);
  const reqKey = `${serviceProp ?? ""}|${envParamProp}`;
  const shownKey = `${shownRes.service ?? ""}|${shownRes.env}`;
  if (reqKey !== shownKey && (!anyDirty || !shownRes.service)) {
    setShownRes(requested);
  }
  const holding = reqKey !== shownKey && anyDirty && !!shownRes.service;
  const { guard } = leave;
  useEffect(() => {
    if (!holding || declined === reqKey) return;
    const held = shownRes;
    guard(
      serviceProp ? "switching to another service" : "closing",
      () => setShownRes({ service: serviceProp, env: envParamProp }),
      () => {
        setDeclined(reqKey);
        if (held.service) onRestore?.(held.service, held.env);
      },
    );
  }, [holding, declined, reqKey, shownRes, serviceProp, envParamProp, guard, onRestore]);
  const service = shownRes.service;
  const envParam = shownRes.env;
  const open = !!service;

  const guardedClose = useCallback(() => {
    guard("closing", () => {
      setPanels({});
      onClose?.();
    });
  }, [guard, onClose]);

  const tabRef = useRef(tab);
  tabRef.current = tab;
  const onTabChangeRef = useRef(onTabChange);
  onTabChangeRef.current = onTabChange;
  const switchTab = useCallback((next: Tab, anchor?: string) => {
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
      if (next === tabRef.current) {
        switchTab(next, anchor);
        return;
      }
      guard("switching tabs", () => switchTab(next, anchor));
    },
    [switchTab, guard],
  );

  const openPage = useCallback(() => {
    if (!onOpenPage) return;
    guard("opening the full page", () => {
      setPanels({});
      onOpenPage(tabRef.current);
    });
  }, [guard, onOpenPage]);

  // Land on the requested tab (?tab=, right-click "View logs" → Logs),
  // else the last-used tab in this session, else Deployments. Re-runs on
  // Back/Forward because the URL's tab flows back in as defaultTab.
  useEffect(() => {
    if (!service) return;
    const requestedTab = resolveTab(defaultTab);
    if (requestedTab) {
      setTab(requestedTab);
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

  // The FailureBanner only shows on the tab the failure deep-link routed
  // to; switching tabs is the user saying they're done with the hint.
  const failureTab = failureKind ? resolveTab(defaultTab) : undefined;

  useEffect(() => {
    try {
      window.sessionStorage.setItem("kuso-service-overlay-tab", tab);
    } catch {
      // Storage blocked: the URL still carries the tab.
    }
  }, [tab]);

  useEffect(() => {
    if (!holding) setDeclined(null);
  }, [holding]);

  // Escape closes the peek. The full page has nothing to close.
  useEffect(() => {
    if (!open || !onClose) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (escapeHandledAbove(e, rootRef.current?.closest<HTMLElement>("[role=dialog]") ?? rootRef.current)) return;
      guardedClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose, guardedClose]);

  const svc = useService(project, service ?? "");
  const drift = useDrift(project, service ?? "");
  const envs = useEnvironments(project);
  // Crons + Runs only show when the service has at least one (or the
  // user can create them). Shares cache keys with the panels.
  const crons = useServiceCrons(project, service ?? "");
  const runs = useRuns(project, service ?? "");
  const builds = useBuilds(project, service ?? "");
  // Build-time failure kinds carry the classifier's lineHint + remediation
  // on the build record; the bell link only has ?kind=.
  const failureClass = useMemo(
    () =>
      failureKind
        ? (builds.data ?? []).find((b) => b.failureClass?.kind === failureKind)?.failureClass
        : undefined,
    [builds.data, failureKind],
  );
  const canCreateJobs = useCanOnProject(project, Perms.SecretsRead);
  const showCrons = canCreateJobs || (crons.data?.length ?? -1) !== 0;
  const showRuns = canCreateJobs || (runs.data?.length ?? -1) !== 0;
  // Shell is project-admin only on the server; a terminal that can only
  // 403 on connect is worse than no tab.
  const canShell = useCanOnProject(project, Perms.ShellExec);
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
    panelRef: rootRef,
    restoreKey: service ? `service:${service}` : undefined,
    manageFocus: !page,
    keys: onOpenPage ? { o: openPage } : undefined,
    chords: Object.fromEntries(
      [...MAIN_TABS, PINNED_TAB].filter((t) => t.chord).map((t) => [t.chord as string, () => guardedSetTab(t.id)]),
    ),
  });

  // If the visibility-driven tab list drops the current tab (the last
  // cron deleted), fall back to Deployments. Only on the true→false edge,
  // so a deep link into an empty Crons tab can still add the first one.
  const prevShowCrons = useRef(showCrons);
  const prevShowRuns = useRef(showRuns);
  useEffect(() => {
    if (tab === "settings") return;
    if (tab === "crons" && prevShowCrons.current && !showCrons) setTab("deployments");
    if (tab === "runs" && prevShowRuns.current && !showRuns) setTab("deployments");
    if (tab === "shell" && !canShell) setTab("deployments");
    prevShowCrons.current = showCrons;
    prevShowRuns.current = showRuns;
  }, [tab, showCrons, showRuns, canShell]);
  const fqn = service ? project + "-" + service : "";
  // The env CR for (service, envParam), matched on the canonical
  // kuso.sislelabs.com/env label; a staging clone carries
  // spec.kind="production" too, so spec.kind is only a legacy fallback.
  const env = (envs.data ?? []).find((e) => {
    if (e.spec.service !== fqn) return false;
    const envLabel = (e.metadata.labels ?? {})["kuso.sislelabs.com/env"];
    if (envParam === "production") {
      if (envLabel) return envLabel === "production";
      return e.spec.kind === "production";
    }
    if (envLabel) return envLabel === envParam;
    const parts = e.metadata.name.split("-");
    return parts[parts.length - 1] === envParam;
  });

  // URL fallback chain, mirroring the canvas ServiceNode so the header
  // and the node agree: internal → none; env custom domain; env auto
  // host; service custom domain; legacy env.status.url.
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
  // Same host the project card and canvas show while a custom domain is still pending.
  const liveUrl = useLiveServiceUrl(project, service ?? "", env, url) ?? url;
  const rollingNow = !!drift.data?.rolloutPending;
  const rolloutSlow = useRolloutSlow(rollingNow, drift.data?.lastSpecMutation);
  // The same reading as the canvas node: the server rollup when present,
  // client heuristics on older servers.
  const derived = deriveServiceStatus({ stopped: !!svc.data?.spec?.stopped, env });
  const stateDetail = env?.status?.stateDetail || undefined;
  const replicas = env?.status?.replicas as { ready?: number; desired?: number } | undefined;
  const shortOfReplicas =
    replicas?.desired !== undefined && replicas.desired > 0 && (replicas.ready ?? 0) < replicas.desired;
  const rollingLegacy = !env?.status?.state && derived.kind === "live" && rollingNow;
  const status: ServiceStatus =
    derived.kind === "live" && shortOfReplicas ? "degraded" : rollingLegacy ? "deploying" : NODE_TO_STATUS[derived.kind];
  const statusDetail =
    status === "stuck"
      ? stateDetail || stuckFacts(env?.status)?.summary || serverStateCause("stuck")
      : rollingLegacy
        ? "Rolling out a new spec"
        : stateDetail || serverStateCause(env?.status?.state);

  const envGroup = env ? envGroupName(env) : envParam;
  const stopped = !!svc.data?.spec?.stopped;
  const problem = healthProblem(env?.status?.state, status === "failing" ? "failed" : status);
  const restartNeeded = needsRestart(drift.data, env?.status?.state);
  const failureBannerVisible = !!failureKind && tab === failureTab;
  const firstRun = useMemo(() => firstRunState(env, builds.data), [env, builds.data]);
  const neverRan = firstRun.kind === "never";
  const isWorker = svc.data?.spec?.runtime === "worker";

  const navigateTo = useCallback(
    (slug: string, anchor?: string) => {
      const next = resolveTab(slug);
      if (!next) return;
      guardedSetTab(next, anchor);
    },
    [guardedSetTab],
  );
  const firstRunCtx = useMemo(() => ({ firstRun, onNavigate: navigateTo }), [firstRun, navigateTo]);

  const label = `Service ${svc.data?.spec.displayName?.trim() || service || ""}`;

  const diagnosticChip = (() => {
    // Purely actionable diagnostics; "rolling" itself is on the status pill.
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
    // A stuck rollout already has its own banner with the reason.
    if (rolling && derived.kind === "stuck") return null;
    if (rolling && rolloutSlow) {
      return (
        <span className="inline-flex items-center gap-1.5 rounded-md border border-[var(--warning)]/40 bg-[var(--warning-subtle)] px-2 py-0.5 text-xs text-[var(--warning)]">
          <AlertTriangle aria-hidden className="h-3 w-3" />
          Rollout taking longer than expected
          <span aria-hidden>·</span>
          <button type="button" onClick={() => navigateTo("deployments", "pod-events")} className="underline underline-offset-2">
            View events
          </button>
        </span>
      );
    }
    if (rolling) {
      // Naming the in-flight field (envVars/image/…) makes it obvious the
      // edit was accepted and is propagating.
      const fields = stale ? d.podsStale : [];
      const what = fields.length > 0 ? ` (${fields.join(", ")})` : "";
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
    if (specOff && !neverRan) {
      return (
        <span
          className="inline-flex items-center gap-1 rounded-md border border-[var(--info)]/40 bg-[var(--info-subtle)] px-2 py-0.5 text-xs text-[var(--info)]"
          title={`Spec out of sync on: ${d.specPending.join(", ")}`}
        >
          Pending changes
        </span>
      );
    }
    if (stale && restartNeeded && service && !neverRan) {
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
  })();

  const actions = service ? (
    <>
      {status === "sleeping" && !stopped ? (
        <WakeControl project={project} service={service} env={wakeEnvParam(env, envGroup)} />
      ) : null}
      <ServiceHeaderActions
        project={project}
        service={service}
        env={env}
        stopped={stopped}
        showRestart={!!env && !restartNeeded && status !== "sleeping" && problem !== "runtime"}
        hideDeploy={neverRan}
        neverRan={neverRan}
      />
    </>
  ) : null;

  const urlLine = liveUrl ? (
    <UrlPill url={liveUrl} />
  ) : isWorker ? (
    <span className="text-[var(--text-tertiary)]">Background worker · no public endpoint</span>
  ) : svcInternal ? (
    <span className="text-[var(--text-tertiary)]">Internal only · no public endpoint</span>
  ) : (
    <span className="text-[var(--text-tertiary)]">No public URL yet</span>
  );

  const titleRow = service ? (
    <EditableTitle
      project={project}
      service={service}
      displayName={svc.data?.spec.displayName?.trim() || ""}
      size={page ? "lg" : "md"}
    />
  ) : null;

  const header = page ? (
    <header className="shrink-0 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40">
      <div className="flex flex-col gap-3 px-4 pt-4 pb-3 sm:px-6 lg:flex-row lg:items-start lg:gap-6 lg:px-8 lg:pt-5">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <span className="mt-0.5 inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-elevated)] text-[var(--text-primary)] shadow-[var(--shadow-sm)]">
            <RuntimeIcon runtime={svc.data?.spec.runtime} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
              {titleRow}
              <SharedStatusPill status={SERVICE_STATUS[status]} detail={statusDetail} />
            </div>
            <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
              {svc.data?.spec.displayName?.trim() && svc.data.spec.displayName.trim() !== service ? (
                <span className="font-mono text-[var(--text-tertiary)]">{service}</span>
              ) : null}
              {envParam !== "production" ? (
                <span className="rounded border border-[var(--border-subtle)] px-1.5 py-px">{envParam}</span>
              ) : null}
              {urlLine}
            </div>
          </div>
        </div>
        <div className="hidden shrink-0 flex-wrap items-center gap-1.5 sm:flex lg:justify-end lg:pt-1">{actions}</div>
      </div>
      {service ? (
        <div className="px-4 pb-3 sm:px-6 lg:px-8">
          <ServiceHealthStrip project={project} service={service} env={env} envGroup={envGroup} status={SERVICE_STATUS[status]} firstRun={firstRun} onNavigate={navigateTo} />
          {diagnosticChip ? <div className="mt-2.5 flex flex-wrap items-center gap-1.5">{diagnosticChip}</div> : null}
        </div>
      ) : null}
    </header>
  ) : (
    <header className="flex shrink-0 items-start gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-3 sm:gap-3 sm:px-5 sm:py-4">
      <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-primary)]">
        <RuntimeIcon runtime={svc.data?.spec.runtime} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          {titleRow}
          <SharedStatusPill status={SERVICE_STATUS[status]} detail={statusDetail} />
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
          {urlLine}
        </div>
        {service ? (
          <ServiceHealthStrip project={project} service={service} env={env} envGroup={envGroup} status={SERVICE_STATUS[status]} firstRun={firstRun} onNavigate={navigateTo} />
        ) : null}
        {diagnosticChip ? <div className="mt-2.5 flex flex-wrap items-center gap-1.5">{diagnosticChip}</div> : null}
        {/* Actions keep one row; what doesn't fit lives in ⋯. */}
        <div className="mt-2 hidden flex-nowrap items-center gap-1.5 sm:flex">{actions}</div>
      </div>
      {onOpenPage ? (
        <button
          type="button"
          onClick={openPage}
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
          onClick={guardedClose}
          aria-label="Close"
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      ) : null}
    </header>
  );

  const pad = page ? "px-4 py-5 sm:px-6 lg:px-8 lg:py-6" : "p-5";
  const width = page ? cn("mx-auto w-full", PAGE_WIDTH[tab] ?? "max-w-5xl") : undefined;
  const section = (children: React.ReactNode, fill = false) => (
    <div className={cn(pad, width, fill && "h-full")}>{children}</div>
  );

  // A link to a deleted or mistyped service: say so instead of drawing a
  // workspace full of actions for something that isn't there.
  const missing = svc.isError && classifyError(svc.error) === "not-found";

  const content = !open ? null : missing ? (
    <div ref={rootRef} tabIndex={-1} className="relative flex h-full min-h-0 flex-col outline-none">
      {onClose ? (
        <button
          type="button"
          onClick={guardedClose}
          aria-label="Close"
          className="absolute right-3 top-3 inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <X className="h-4 w-4" />
        </button>
      ) : null}
      <div className="p-6 lg:p-10">
        <EmptyState
          icon={<PackageX className="h-5 w-5" />}
          title={`No service called “${service}”`}
          description={`${project} has no service by that name in ${envParam}. It may have been deleted or renamed.`}
          action={
            <Link
              href={projectCanvasHref(project, envParam)}
              className="inline-flex h-8 items-center rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] hover:bg-[var(--btn-primary-bg-hover)]"
            >
              Back to {project}
            </Link>
          }
        />
      </div>
    </div>
  ) : (
    <div ref={rootRef} tabIndex={-1} className="flex h-full min-h-0 flex-col outline-none">
      <FirstRunProvider value={firstRunCtx}>
        {header}

        {service && problem && !failureBannerVisible && !neverRan ? (
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
            wide={page}
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

        <div
          id="svc-overlay-panel"
          role="tabpanel"
          aria-labelledby={`svc-overlay-tab-${tab}`}
          className="relative flex-1 min-h-0 overflow-hidden"
        >
          {svc.isPending ? (
            <div className={cn("space-y-3", pad, width)}>
              <Skeleton className="h-8 w-48" />
              <Skeleton className="h-32 w-full" />
              <Skeleton className="h-32 w-full" />
            </div>
          ) : svc.isError ? (
            <p className={cn("text-sm text-[var(--error)]", pad, width)}>Failed to load service: {svc.error?.message}</p>
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
                  {failureBannerVisible && (
                    <div className={cn(page ? "px-4 pt-5 sm:px-6 lg:px-8" : "px-5 pt-5", width)}>
                      <FailureBanner
                        kind={failureKind}
                        lineHint={failureClass?.lineHint}
                        remediation={failureClass?.remediation}
                        onNavigate={navigateTo}
                      />
                    </div>
                  )}
                  {tab === "deployments" &&
                    section(
                      <ServiceDeploymentsPanel project={project} service={service ?? ""} env={env} url={liveUrl} onNavigate={navigateTo} />,
                    )}
                  {tab === "variables" &&
                    section(
                      <ServiceVariablesPanel
                        project={project}
                        service={service ?? ""}
                        env={env?.metadata?.labels?.["kuso.sislelabs.com/env"] ?? env?.spec?.kind ?? "production"}
                      />,
                    )}
                  {tab === "metrics" &&
                    section(
                      <ServiceMetricsPanel
                        project={project}
                        service={service ?? ""}
                        env={env}
                        memoryLimit={svc.data?.spec.resources?.limits?.memory}
                        onViewLogs={() => guardedSetTab("logs")}
                      />,
                    )}
                  {tab === "logs" &&
                    section(
                      <ServiceLogsPanel
                        key={env?.metadata.name ?? "none"}
                        project={project}
                        service={service ?? ""}
                        onOpenDeployments={() => guardedSetTab("deployments")}
                        defaultEnv={env ? envGroupName(env) : ""}
                        streamEnv={env?.metadata.name}
                        fill={page}
                      />,
                      page,
                    )}
                  {tab === "errors" &&
                    section(
                      <ServiceErrorsPanel
                        key={envGroup}
                        project={project}
                        service={service ?? ""}
                        onViewLogs={() => guardedSetTab("logs")}
                        defaultEnv={envGroup}
                      />,
                    )}
                  {tab === "vulnerabilities" &&
                    section(
                      <ServiceVulnerabilitiesPanel
                        key={`${service ?? ""}/${env ? envGroupName(env) : envParam}`}
                        project={project}
                        service={service ?? ""}
                        env={env ? envGroupName(env) : envParam}
                      />,
                    )}
                  {tab === "shell" &&
                    section(
                      <ServiceTerminalPanel
                        project={project}
                        service={service ?? ""}
                        env={envParam}
                        blocked={stopped ? "stopped" : status === "sleeping" ? "asleep" : undefined}
                        wakeEnv={wakeEnvParam(env, envGroup)}
                      />,
                    )}
                  {tab === "crons" &&
                    section(
                      <ServiceCronsPanel
                        project={project}
                        service={service ?? ""}
                        // Deep link (canvas "Add cron…") into an empty
                        // list opens the create form straight away.
                        defaultAdding={defaultTab === "crons" && crons.data?.length === 0}
                      />,
                    )}
                  {tab === "runs" && section(<ServiceRunsPanel project={project} service={service ?? ""} />)}
                  {tab === "settings" && (
                    // The settings panel pads itself (sticky section nav +
                    // sectioned form). Keyed on identity so switching to
                    // another service can't carry this form's edits onto it.
                    <div className={width}>
                      <ServiceSettingsPanel
                        key={`${project}/${service ?? ""}/${envParam ?? ""}`}
                        project={project}
                        service={service ?? ""}
                        svc={svc.data}
                        env={envParam}
                      />
                    </div>
                  )}
                </motion.div>
              </AnimatePresence>
              <SaveBar entry={panels[tab]} />
            </OverlayDirtyContext.Provider>
          )}
        </div>
        {/* Phones: the actions live in a bar pinned to the bottom. */}
        {service ? (
          <div className="flex shrink-0 items-center gap-1.5 border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] empty:hidden sm:hidden">
            {actions}
          </div>
        ) : null}
      </FirstRunProvider>
    </div>
  );

  return (
    <>
      {frame ? frame({ open, label, content, requestClose: guardedClose, tab }) : content}
      {leave.dialog}
    </>
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
  stuck: "Stuck",
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
  wide = false,
}: {
  wide?: boolean;
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
  const danger = runtime || problem === "stuck";
  const shown = trimRepeatedCause(label, detail);
  return (
    <div
      role="alert"
      className={cn(
        "flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2",
        wide ? "sm:px-6 lg:px-8" : "sm:px-5",
        danger
          ? "border-[var(--error)]/30 bg-[var(--error)]/5"
          : "border-[var(--warning)]/30 bg-[var(--warning)]/5",
      )}
    >
      <AlertTriangle
        className={cn("h-3.5 w-3.5 shrink-0", danger ? "text-[var(--error)]" : "text-[var(--warning)]")}
      />
      <span className="text-xs font-medium text-[var(--text-primary)]">{label}</span>
      {shown ? (
        <span className="min-w-0 flex-1 truncate text-xs text-[var(--text-secondary)]" title={shown}>
          {shown}
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

// The canvas node's kinds on the shared vocabulary; the specific cause
// (crashloop, failed build) is the detail, not the label.
const NODE_TO_STATUS: Record<NodeStatusKind, ServiceStatus> = {
  live: "live",
  degraded: "degraded",
  building: "building",
  deploying: "deploying",
  build_failed: "failing",
  crashing: "failing",
  stuck: "stuck",
  sleeping: "sleeping",
  stopped: "stopped",
  not_deployed: "not_deployed",
  provisioning: "deploying",
  paused: "stopped",
  unknown: "unknown",
};

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
