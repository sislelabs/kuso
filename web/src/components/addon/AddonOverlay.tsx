"use client";

import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useAddons } from "@/features/projects";
import { Skeleton } from "@/components/ui/skeleton";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { useCan, Perms } from "@/features/auth";
import { X, Database, HardDrive, Settings, Info } from "lucide-react";
import { cn } from "@/lib/utils";

import { OverviewTab } from "./overlay/OverviewTab";
import { BackupsTab } from "./overlay/BackupsTab";
import { SQLTab } from "./overlay/SQLTab";
import { SettingsTab } from "./overlay/SettingsTab";

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
export function AddonOverlay({ project, addon, onClose }: Props) {
  const open = !!addon;
  const [tab, setTab] = useState<Tab>("overview");
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (addon) setTab("overview");
  }, [addon]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
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
  const canSQL = useCan(Perms.SQLRead);
  const canWriteAddon = useCan(Perms.AddonsWrite);

  return (
    <AnimatePresence>
      {open && (
        <div className="fixed inset-0 z-50 flex" role="dialog" aria-modal="true">
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
              <button
                type="button"
                onClick={onClose}
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
                    onClick={() => setTab(t.id)}
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

            <div className="min-h-0 flex-1 overflow-y-auto">
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
                <SettingsTab project={project} addon={addon!} cr={data} onClose={onClose} />
              )}
            </div>
          </motion.div>
        </div>
      )}
    </AnimatePresence>
  );
}
