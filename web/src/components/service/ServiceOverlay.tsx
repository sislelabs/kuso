"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useService, useDrift } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { ServiceDeploymentsPanel } from "./overlay/ServiceDeploymentsPanel";
import { ServiceVariablesPanel } from "./overlay/ServiceVariablesPanel";
import { ServiceMetricsPanel } from "./overlay/ServiceMetricsPanel";
import { ServiceCronsPanel } from "./overlay/ServiceCronsPanel";
import { ServiceLogsPanel } from "./overlay/ServiceLogsPanel";
import { ServiceErrorsPanel } from "./overlay/ServiceErrorsPanel";
import { ServiceSettingsPanel } from "./overlay/ServiceSettingsPanel";
import { Check, Copy, ExternalLink, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

// OverlayDirtyContext lets every panel inside ServiceOverlay register
// whether its form has unsaved edits. The shell uses the union of
// those flags to gate close/ESC/tab-switch behind a "Discard changes?"
// confirm — without this, an inadvertent ESC silently lost in-progress
// env-var edits.
type OverlayDirtyAPI = {
  setPanelDirty: (key: string, dirty: boolean) => void;
};
const OverlayDirtyContext = createContext<OverlayDirtyAPI | null>(null);
export function useOverlayDirty(panelKey: string, dirty: boolean) {
  const api = useContext(OverlayDirtyContext);
  useEffect(() => {
    if (!api) return;
    api.setPanelDirty(panelKey, dirty);
    return () => api.setPanelDirty(panelKey, false);
  }, [api, panelKey, dirty]);
}

type Tab = "deployments" | "variables" | "metrics" | "logs" | "errors" | "crons" | "settings";
// Settings is pinned to the right of the strip (rendered outside
// the scrollable container) because it holds the destructive
// actions — Delete service, change runtime, change port, change
// scale. At 1280px the seven-peer-tab layout used to crop Settings
// behind the scroll edge; users had to scroll to discover the
// most-important destination. Pinning it surfaces the affordance
// at any viewport.
const MAIN_TABS: { id: Tab; label: string }[] = [
  { id: "deployments", label: "Deployments" },
  { id: "variables", label: "Variables" },
  { id: "metrics", label: "Metrics" },
  { id: "logs", label: "Logs" },
  { id: "errors", label: "Errors" },
  { id: "crons", label: "Crons" },
];
const PINNED_TAB: { id: Tab; label: string } = { id: "settings", label: "Settings" };

interface Props {
  project: string;
  service: string | null;
  env?: string; // "production" | preview short name
  // defaultTab lets canvas right-click menus deep-link into a specific
  // tab (Logs, Errors, …). Undefined falls back to the per-service
  // default of "deployments".
  defaultTab?: string;
  onClose: () => void;
}

// ServiceOverlay is the in-page inspector shown when a service is
// clicked on the canvas/list. No URL — clicking outside or pressing
// ESC closes it. Slides in from the right with a spring; the dimmed
// backdrop is its own click target so peripheral clicks dismiss the
// panel without bubbling into canvas pan/drag.
export function ServiceOverlay({ project, service, env: envParam = "production", defaultTab, onClose }: Props) {
  const open = !!service;
  const [tab, setTab] = useState<Tab>("deployments");
  const panelRef = useRef<HTMLDivElement>(null);

  // Per-panel dirty registry. Children call useOverlayDirty(key, bool);
  // we keep the ref-mirrored snapshot so onClose/ESC can read the
  // current state without depending on a render. The setter triggers
  // a state update so the discard banner can render.
  const dirtyMap = useRef<Record<string, boolean>>({});
  // Kept for the API stability of useOverlayDirty's setPanelDirty —
  // panels still call into the registry, the value just no longer
  // gates close/tab-switch (we removed the window.confirm prompt).
  const [, setHasDirtyPanel] = useState(false);
  const dirtyAPI = useMemo<OverlayDirtyAPI>(
    () => ({
      setPanelDirty: (key, dirty) => {
        if (dirty) dirtyMap.current[key] = true;
        else delete dirtyMap.current[key];
        setHasDirtyPanel(Object.keys(dirtyMap.current).length > 0);
      },
    }),
    []
  );

  // Close + tab-switch unconditionally drop dirty state. The previous
  // window.confirm("Discard unsaved changes?") was annoying enough
  // that users asked for it gone — closing an overlay or switching
  // tabs is fast, and the floating Save bar's pip already signals
  // unsaved state. If a user really loses an edit they didn't mean
  // to discard, the inline dirty-pip + Save bar should have been the
  // signal, not a browser-modal interrupt.
  const guardedClose = useCallback(() => {
    dirtyMap.current = {};
    setHasDirtyPanel(false);
    onClose();
  }, [onClose]);

  const guardedSetTab = useCallback((next: Tab) => {
    dirtyMap.current = {};
    setHasDirtyPanel(false);
    setTab(next);
  }, []);

  // When a service opens, land on the requested tab (right-click "View
  // logs" → Logs, etc.) or fall back to the user's last-used tab in
  // this session. Falls back to Deployments — the most actionable
  // default — when there's no remembered tab. Iterating across
  // services to compare logs no longer punishes the user with a
  // forced "back to Deployments" reset on every open.
  useEffect(() => {
    if (!service) return;
    const valid = [...MAIN_TABS, PINNED_TAB].some((t) => t.id === defaultTab);
    if (valid) {
      setTab(defaultTab as Tab);
      return;
    }
    let remembered: Tab = "deployments";
    if (typeof window !== "undefined") {
      const v = window.sessionStorage.getItem("kuso-service-overlay-tab");
      if (v && [...MAIN_TABS, PINNED_TAB].some((t) => t.id === v)) remembered = v as Tab;
    }
    setTab(remembered);
  }, [service, defaultTab]);

  // Persist tab selection so the next open in this session lands on
  // the same place. SessionStorage (not localStorage) so a new tab/
  // window starts fresh.
  useEffect(() => {
    if (typeof window === "undefined") return;
    window.sessionStorage.setItem("kuso-service-overlay-tab", tab);
  }, [tab]);

  // Close on ESC + lock body scroll while open.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") guardedClose();
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
  const fqn = service ? project + "-" + service : "";
  const env = (envs.data ?? []).find((e) => {
    if (e.spec.service !== fqn) return false;
    if (envParam === "production") return e.spec.kind === "production";
    const short = e.metadata.name.split("-").slice(-2).join("-");
    return short === envParam;
  });

  const url = env?.status?.url as string | undefined;
  const ready = !!env?.status?.ready;
  const phase = (env?.status?.phase as string | undefined)?.toLowerCase();
  const status =
    phase === "building" || phase === "deploying"
      ? "building"
      : ready
        ? "active"
        : phase === "failed" || phase === "error"
          ? "failed"
          : phase === "sleeping"
            ? "sleeping"
            : "unknown";

  return (
    <AnimatePresence>
      {open && (
        <div className="fixed inset-0 z-50 flex" role="dialog" aria-modal="true">
          {/* Backdrop — clickable to close. */}
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
          {/* Panel — slides from the right. `relative z-10` lifts it
              above the absolutely-positioned backdrop, otherwise the
              panel sits in normal flow *behind* the backdrop and gets
              filtered by its blur. */}
          <motion.div
            ref={panelRef}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            className="relative z-10 ml-auto flex h-full w-full flex-col bg-[var(--bg-primary)] shadow-[var(--shadow-lg)] border-l border-[var(--border-subtle)] sm:max-w-3xl"
          >
            {/* Sticky header. Tighter padding on small screens so the
                overlay's title row uses the full width of a phone. */}
            <header className="flex shrink-0 items-start gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-3 sm:gap-3 sm:px-5 sm:py-4">
              <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-primary)]">
                <RuntimeIcon runtime={svc.data?.spec.runtime} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <h2 className="font-heading text-lg font-semibold tracking-tight truncate">
                    {/* Show the user's display name when set; fall back
                        to the URL slug. The slug appears below in mono
                        next to the project label so the actual CR name
                        + URL are still discoverable. */}
                    {svc.data?.spec.displayName?.trim() || service || ""}
                  </h2>
                  <StatusDot status={status} />
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-2 text-[10px]">
                  <span className="font-mono uppercase tracking-widest text-[var(--text-tertiary)]">
                    {project}
                    {svc.data?.spec.displayName?.trim() && service ? (
                      <>
                        {" · "}
                        <span className="text-[var(--text-secondary)]">{service}</span>
                      </>
                    ) : null}
                  </span>
                  {url ? (
                    <UrlPill url={url} />
                  ) : (
                    <span className="font-mono text-[10px] text-[var(--text-tertiary)]">no URL yet</span>
                  )}
                  {/* Drift chip. Priority order matters:
                        1. rolloutPending — kube has a new ReplicaSet
                           in flight. Even if podsStale is also true
                           (it always is during the rollout window),
                           the right copy is "rolling out…", not
                           "restart needed" — kube IS auto-rolling.
                           Blue.
                        2. specPending — service spec ↔ env CR
                           mismatch. Propagation bug; shouldn't appear
                           in steady state. Amber + "pending changes".
                        3. podsStale w/o rolloutPending — pod env
                           differs from spec AND no rollout in
                           progress, which means kube isn't going to
                           roll on its own (rare; only happens for
                           non-template fields). Amber + "restart
                           needed". User must click Redeploy.
                      Hidden when nothing's pending. */}
                  {(() => {
                    const d = drift.data;
                    if (!d) return null;
                    const stale = d.podsStale && d.podsStale.length > 0;
                    const rolling = d.rolloutPending;
                    const specOff = d.specPending && d.specPending.length > 0;
                    // Helm-operator failure chip wins over rolling/stale —
                    // it's the actual root cause when the spec edit isn't
                    // taking, and "rolling out" is misleading if the
                    // chart never rendered.
                    if (d.helmError && d.helmError.length > 0) {
                      return (
                        <span
                          className="inline-flex max-w-[40ch] items-center gap-1 truncate rounded-md border border-red-500/40 bg-red-500/10 px-2 py-0.5 font-mono text-[10px] text-red-200"
                          title={d.helmError}
                        >
                          helm: {d.helmError}
                        </span>
                      );
                    }
                    if (!stale && !rolling && !specOff) return null;
                    let label: string;
                    let title: string;
                    let cls: string;
                    if (rolling) {
                      label = "rolling out…";
                      cls = "border-blue-500/40 bg-blue-500/10 text-blue-200";
                      title = "kube is rolling new pods with the latest spec";
                    } else if (specOff) {
                      label = "pending changes";
                      cls = "border-amber-500/40 bg-amber-500/10 text-amber-200";
                      title = `Spec out of sync on: ${d.specPending.join(", ")}`;
                    } else {
                      // stale && !rolling: kube isn't going to roll.
                      // One-click resolution path — don't make this
                      // sound scarier than it is.
                      label = "pending restart — redeploy to apply";
                      cls = "border-amber-500/40 bg-amber-500/10 text-amber-200";
                      title =
                        `Pod still running old ${d.podsStale.join(", ")}. ` +
                        `Open Deployments and click Redeploy to roll.`;
                    }
                    // The "pending restart" case has a 1-click fix on
                    // the Deployments tab; render it as a button so a
                    // user can jump straight there. The other cases
                    // are read-only state, so a span is fine.
                    if (stale && !rolling && !specOff) {
                      return (
                        <button
                          type="button"
                          onClick={() => guardedSetTab("deployments")}
                          className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-[10px] hover:brightness-110 ${cls}`}
                          title={title}
                        >
                          {label}
                        </button>
                      );
                    }
                    return (
                      <span
                        className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-[10px] ${cls}`}
                        title={title}
                      >
                        {label}
                      </span>
                    );
                  })()}
                </div>
              </div>
              <button
                type="button"
                onClick={guardedClose}
                aria-label="Close"
                className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-4 w-4" />
              </button>
            </header>

            {/* Tab strip: scrollable left rail for the view tabs,
                pinned Settings on the right. The left rail scrolls
                horizontally on narrow viewports; Settings stays
                visible so the destructive actions are always one
                click away regardless of width. */}
            <nav className="flex shrink-0 items-center border-b border-[var(--border-subtle)] px-2 sm:px-3">
              <div className="flex flex-1 min-w-0 flex-nowrap items-center gap-1 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
                {MAIN_TABS.map((t) => {
                  const active = t.id === tab;
                  return (
                    <button
                      key={t.id}
                      type="button"
                      ref={(el) => {
                        if (active && el) {
                          el.scrollIntoView({ inline: "center", block: "nearest", behavior: "smooth" });
                        }
                      }}
                      onClick={() => guardedSetTab(t.id)}
                      className={cn(
                        "relative inline-flex h-10 shrink-0 items-center px-3 text-sm font-medium transition-colors whitespace-nowrap",
                        active
                          ? "text-[var(--text-primary)]"
                          : "text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                      )}
                    >
                      {t.label}
                      {active && (
                        <motion.span
                          layoutId="overlay-tab-underline"
                          className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--text-primary)]"
                          transition={{ type: "spring", stiffness: 380, damping: 32 }}
                        />
                      )}
                    </button>
                  );
                })}
              </div>
              <button
                type="button"
                onClick={() => guardedSetTab(PINNED_TAB.id)}
                className={cn(
                  "relative ml-2 inline-flex h-10 shrink-0 items-center border-l border-[var(--border-subtle)] pl-3 pr-2 text-sm font-medium transition-colors whitespace-nowrap",
                  tab === PINNED_TAB.id
                    ? "text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                )}
              >
                {PINNED_TAB.label}
                {tab === PINNED_TAB.id && (
                  <motion.span
                    layoutId="overlay-tab-underline"
                    className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--text-primary)]"
                    transition={{ type: "spring", stiffness: 380, damping: 32 }}
                  />
                )}
              </button>
            </nav>

            {/* Body — switches by tab. Wraps in motion.div so each
                tab swap fades, but the body itself owns its own scroll. */}
            <div className="flex-1 min-h-0 overflow-hidden">
              {svc.isPending ? (
                <div className="space-y-3 p-6">
                  <Skeleton className="h-8 w-48" />
                  <Skeleton className="h-32 w-full" />
                  <Skeleton className="h-32 w-full" />
                </div>
              ) : svc.isError ? (
                <p className="p-6 text-sm text-red-400">
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
                    {tab === "deployments" && (
                      <div className="p-5">
                        <ServiceDeploymentsPanel project={project} service={service ?? ""} env={env} />
                      </div>
                    )}
                    {tab === "variables" && (
                      <div className="p-5">
                        <ServiceVariablesPanel project={project} service={service ?? ""} />
                      </div>
                    )}
                    {tab === "metrics" && (
                      <div className="p-5">
                        <ServiceMetricsPanel project={project} service={service ?? ""} />
                      </div>
                    )}
                    {tab === "logs" && (
                      <div className="p-5">
                        <ServiceLogsPanel project={project} service={service ?? ""} />
                      </div>
                    )}
                    {tab === "errors" && (
                      <div className="p-5">
                        <ServiceErrorsPanel project={project} service={service ?? ""} />
                      </div>
                    )}
                    {tab === "crons" && (
                      <div className="p-5">
                        <ServiceCronsPanel project={project} service={service ?? ""} />
                      </div>
                    )}
                    {tab === "settings" && (
                      // ServiceSettingsPanel handles its own padding
                      // (sticky sidebar nav + sectioned form). Wrapping
                      // in p-5 here would double-pad and break the
                      // sticky-positioning math.
                      <ServiceSettingsPanel project={project} service={service ?? ""} svc={svc.data} env={envParam} />
                    )}
                  </motion.div>
                </AnimatePresence>
                </OverlayDirtyContext.Provider>
              )}
            </div>
          </motion.div>
        </div>
      )}
    </AnimatePresence>
  );
}

function StatusDot({ status }: { status: string }) {
  const map: Record<string, { dot: string; pulse: boolean; label: string }> = {
    active:    { dot: "bg-emerald-400", pulse: false, label: "Active" },
    building:  { dot: "bg-amber-400",   pulse: true,  label: "Building" },
    failed:    { dot: "bg-red-400",     pulse: false, label: "Failed" },
    sleeping:  { dot: "bg-slate-400",   pulse: false, label: "Sleeping" },
    unknown:   { dot: "bg-[var(--text-tertiary)]/50", pulse: false, label: "Idle" },
  };
  const m = map[status] ?? map.unknown;
  return (
    <span
      title={m.label}
      className="relative inline-flex h-2 w-2 shrink-0 items-center justify-center"
    >
      {m.pulse && (
        <span className={cn("absolute inset-0 rounded-full opacity-60 animate-ping", m.dot)} />
      )}
      <span className={cn("relative inline-block h-2 w-2 rounded-full", m.dot)} />
    </span>
  );
}

function UrlPill({ url }: { url: string }) {
  const [copied, setCopied] = useState(false);
  const display = url.replace(/^https?:\/\//, "");

  const onCopy = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      toast.success("URL copied");
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      toast.error("Couldn't copy");
    }
  };

  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] pl-2 pr-1 py-0.5 font-mono text-[10px] text-[var(--text-secondary)]">
      <a
        href={url}
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-1 hover:text-[var(--accent)]"
      >
        {display}
        <ExternalLink className="h-2.5 w-2.5" />
      </a>
      <button
        type="button"
        onClick={onCopy}
        aria-label="Copy URL"
        className="inline-flex h-4 w-4 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-primary)] hover:text-[var(--text-primary)]"
      >
        {copied ? <Check className="h-2.5 w-2.5 text-emerald-400" /> : <Copy className="h-2.5 w-2.5" />}
      </button>
    </span>
  );
}
