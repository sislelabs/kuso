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

  // Scroll the active tab into view when `tab` changes. Previously
  // this lived in the button's ref={el => el.scrollIntoView(...)},
  // which fires on every render (including state updates that don't
  // touch the tab) — so any dirty-state flip or panels-map churn
  // would re-trigger a smooth-scroll animation on the active tab.
  // useEffect keyed on tab fires once per actual change.
  useEffect(() => {
    if (!open) return;
    const root = panelRef.current;
    if (!root) return;
    const el = root.querySelector<HTMLElement>(`[data-tab="${tab}"][data-active="1"]`);
    if (el) el.scrollIntoView({ inline: "center", block: "nearest", behavior: "smooth" });
  }, [tab, open]);

  // Per-panel dirty + save registry. Children call useOverlayDirty
  // and (optionally) supply onSave/onDiscard so the shell can render
  // one SaveBar at the bottom for the active tab, instead of every
  // panel rolling its own button placement.
  const dirtyMap = useRef<Record<string, boolean>>({});
  const [panels, setPanels] = useState<Record<string, PanelEntry>>({});
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

  // Close + tab-switch unconditionally drop dirty state. The previous
  // window.confirm("Discard unsaved changes?") was annoying enough
  // that users asked for it gone — closing an overlay or switching
  // tabs is fast, and the floating Save bar's pip already signals
  // unsaved state. If a user really loses an edit they didn't mean
  // to discard, the inline dirty-pip + Save bar should have been the
  // signal, not a browser-modal interrupt.
  const guardedClose = useCallback(() => {
    dirtyMap.current = {};
    setPanels({});
    onClose();
  }, [onClose]);

  const guardedSetTab = useCallback((next: Tab) => {
    dirtyMap.current = {};
    setPanels({});
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
  // "rolling" is a real running state, not a diagnostic — the
  // overlay used to pack it into the drift chip alongside three
  // other meanings ("pending changes", "restart needed",
  // "helm: <err>"), which conflated state with diagnostics. Surface
  // rolling through StatusDot; the drift chip stays purely
  // diagnostic.
  const rollingNow = !!drift.data?.rolloutPending;
  const status =
    phase === "building" || phase === "deploying"
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
                  {/* Diagnostic chip. The "rolling out" state lives
                      on StatusDot now — this chip exists purely for
                      actionable diagnostics:
                        - helmError: helm-operator failed; user
                          needs to check the spec.
                        - specPending: service spec ↔ env CR mismatch
                          (propagation bug; shouldn't appear in
                          steady state).
                        - podsStale w/o rollout: pod env differs from
                          spec AND no rollout in progress — kube isn't
                          going to roll on its own; user must
                          Redeploy. */}
                  {(() => {
                    const d = drift.data;
                    if (!d) return null;
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
                    const stale = d.podsStale && d.podsStale.length > 0;
                    const rolling = d.rolloutPending;
                    const specOff = d.specPending && d.specPending.length > 0;
                    // Suppress diagnostic during an active rollout —
                    // kube is already resolving it. StatusDot shows
                    // "Rolling" to surface the same fact.
                    if (rolling) return null;
                    if (specOff) {
                      return (
                        <span
                          className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 font-mono text-[10px] text-amber-200"
                          title={`Spec out of sync on: ${d.specPending.join(", ")}`}
                        >
                          pending changes
                        </span>
                      );
                    }
                    if (stale) {
                      return (
                        <button
                          type="button"
                          onClick={() => guardedSetTab("deployments")}
                          className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 font-mono text-[10px] text-amber-200 hover:brightness-110"
                          title={
                            `Pod still running old ${d.podsStale.join(", ")}. ` +
                            `Open Deployments and click Redeploy to roll.`
                          }
                        >
                          pending restart — redeploy to apply
                        </button>
                      );
                    }
                    return null;
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
                      data-tab={t.id}
                      data-active={active ? "1" : undefined}
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
                tab swap fades, but the body itself owns its own scroll.
                Relative for the unified SaveBar's absolute anchor. */}
            <div className="relative flex-1 min-h-0 overflow-hidden">
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
                          <span className="font-mono text-[11px] text-[var(--text-secondary)]">
                            unsaved changes
                          </span>
                          {active.onDiscard && (
                            <button
                              type="button"
                              onClick={() => active.onDiscard?.()}
                              disabled={active.saving}
                              className="font-mono text-[11px] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-50"
                            >
                              discard
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
                            className="font-mono text-[10px] text-red-300 max-w-[40ch] truncate"
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
        </div>
      )}
    </AnimatePresence>
  );
}

function StatusDot({ status }: { status: string }) {
  const map: Record<string, { dot: string; pulse: boolean; label: string }> = {
    active:    { dot: "bg-emerald-400", pulse: false, label: "Active" },
    rolling:   { dot: "bg-blue-400",    pulse: true,  label: "Rolling" },
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
