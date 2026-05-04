"use client";

import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useService } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { ServiceDeploymentsPanel } from "./overlay/ServiceDeploymentsPanel";
import { ServiceVariablesPanel } from "./overlay/ServiceVariablesPanel";
import { ServiceMetricsPanel } from "./overlay/ServiceMetricsPanel";
import { ServiceCronsPanel } from "./overlay/ServiceCronsPanel";
import { ServiceLogsPanel } from "./overlay/ServiceLogsPanel";
import { ServiceSettingsPanel } from "./overlay/ServiceSettingsPanel";
import { Check, Copy, ExternalLink, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

type Tab = "deployments" | "variables" | "metrics" | "logs" | "crons" | "settings";
const TABS: { id: Tab; label: string }[] = [
  { id: "deployments", label: "Deployments" },
  { id: "variables", label: "Variables" },
  { id: "metrics", label: "Metrics" },
  { id: "logs", label: "Logs" },
  { id: "crons", label: "Crons" },
  { id: "settings", label: "Settings" },
];

interface Props {
  project: string;
  service: string | null;
  env?: string; // "production" | preview short name
  onClose: () => void;
}

// ServiceOverlay is the in-page inspector shown when a service is
// clicked on the canvas/list. No URL — clicking outside or pressing
// ESC closes it. Slides in from the right with a spring; the dimmed
// backdrop is its own click target so peripheral clicks dismiss the
// panel without bubbling into canvas pan/drag.
export function ServiceOverlay({ project, service, env: envParam = "production", onClose }: Props) {
  const open = !!service;
  const [tab, setTab] = useState<Tab>("deployments");
  const panelRef = useRef<HTMLDivElement>(null);

  // Reset to Deployments whenever a different service opens so the
  // user lands on the most actionable tab.
  useEffect(() => {
    if (service) setTab("deployments");
  }, [service]);

  // Close on ESC + lock body scroll while open.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [open, onClose]);

  const svc = useService(project, service ?? "");
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
            onClick={onClose}
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
            className="relative z-10 ml-auto flex h-full w-full max-w-3xl flex-col bg-[var(--bg-primary)] shadow-[var(--shadow-lg)] border-l border-[var(--border-subtle)]"
          >
            {/* Sticky header */}
            <header className="flex shrink-0 items-start gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-5 py-4">
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

            {/* Tab strip with sliding indicator */}
            <nav className="flex shrink-0 items-center gap-1 border-b border-[var(--border-subtle)] px-3">
              {TABS.map((t) => {
                const active = t.id === tab;
                return (
                  <button
                    key={t.id}
                    type="button"
                    onClick={() => setTab(t.id)}
                    className={cn(
                      "relative inline-flex h-10 items-center px-3 text-sm font-medium transition-colors",
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
                      <ServiceLogsPanel project={project} service={service ?? ""} />
                    )}
                    {tab === "crons" && (
                      <ServiceCronsPanel project={project} service={service ?? ""} />
                    )}
                    {tab === "settings" && (
                      <ServiceSettingsPanel project={project} service={service ?? ""} svc={svc.data} />
                    )}
                  </motion.div>
                </AnimatePresence>
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
