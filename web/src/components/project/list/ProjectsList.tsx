"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowUpRight, Box, ChevronDown, ExternalLink, Folder, Pause, Power, SearchX, Settings, Star } from "lucide-react";
import {
  useProjectsSummary,
  useStartProject,
  useStopProject,
  type ProjectSummaryItem,
} from "@/features/projects";
import { useCan, Perms } from "@/features/auth";
import { useProjectPrefs, useSetProjectPref } from "@/features/userprefs";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { CanvasContextMenu, type ContextMenuEntry } from "@/components/canvas/CanvasContextMenu";
import { cn } from "@/lib/utils";
import type { KusoProject } from "@/types/projects";
import { ProjectCard } from "./ProjectCard";
import type { ProjectCardActions } from "./ProjectCardMenu";
import { ProjectsToolbar } from "./ProjectsToolbar";
import {
  buildCardModel,
  matchesQuery,
  matchesStatus,
  type ProjectCardModel,
  type StatusFilter,
} from "./projectCardModel";
import { useListPrefs } from "./useListPrefs";

interface Entry {
  model: ProjectCardModel;
  starred: boolean;
  folder: string;
}

const EMPTY_COUNTS: Record<StatusFilter, number> = { all: 0, live: 0, failing: 0, building: 0, sleeping: 0, stopped: 0 };

// ProjectsList renders the toolbar + grouped grid. Every card's health
// comes from ONE batched /api/projects/summary poll (not a per-card
// fan-out).
export function ProjectsList({ projects }: { projects: ReadonlyArray<KusoProject> }) {
  const router = useRouter();
  // Only instance admins manage settings. services:write is project-scoped
  // and never in the session perm set useCan checks, so settings:admin is
  // the honest instance-level gate for stop/start too; the server still
  // enforces the project role.
  const canManage = useCan(Perms.SettingsAdmin);
  const stopProject = useStopProject();
  const startProject = useStartProject();
  const [confirmStop, setConfirmStop] = useState<{ name: string; count: number } | null>(null);
  const [cardMenu, setCardMenu] = useState<{ x: number; y: number; items: ContextMenuEntry[] } | null>(null);
  const [query, setQuery] = useState("");
  const [prefs, setPrefs] = useListPrefs();

  const { byProject } = useProjectPrefs();
  const setPref = useSetProjectPref();
  const summaryQuery = useProjectsSummary();
  const summaryByName = useMemo(() => {
    const m = new Map<string, ProjectSummaryItem>();
    for (const item of summaryQuery.data ?? []) m.set(item.project.metadata.name, item);
    return m;
  }, [summaryQuery.data]);

  const knownFolders = useMemo(
    () =>
      Array.from(new Set(Array.from(byProject.values()).map((p) => p.folder).filter((f): f is string => !!f))).sort(
        (a, b) => a.localeCompare(b),
      ),
    [byProject],
  );

  // Models rebuild only when the summary data / prefs actually change
  // (structural sharing keeps refs stable across identical polls).
  const entries = useMemo<Entry[]>(
    () =>
      projects.map((p) => {
        const pref = byProject.get(p.metadata.name);
        return {
          model: buildCardModel(p, summaryByName.get(p.metadata.name)),
          starred: pref?.starred ?? false,
          folder: pref?.folder ?? "",
        };
      }),
    [projects, byProject, summaryByName],
  );

  const counts = useMemo(() => {
    if (!summaryQuery.data) return null;
    const c = { ...EMPTY_COUNTS, all: entries.length };
    for (const e of entries) {
      const s = e.model.status;
      if (s in c) c[s as Exclude<StatusFilter, "all">]++;
    }
    return c;
  }, [entries, summaryQuery.data]);

  const visible = useMemo(() => {
    const filtered = entries.filter((e) => matchesQuery(e.model, query) && matchesStatus(e.model, prefs.status));
    const byName = (a: Entry, b: Entry) => a.model.name.localeCompare(b.model.name);
    if (prefs.sort === "name") return filtered.sort(byName);
    const ts = (e: Entry) => (e.model.lastDeployAt ? Date.parse(e.model.lastDeployAt) : 0);
    return filtered.sort((a, b) => ts(b) - ts(a) || byName(a, b));
  }, [entries, query, prefs.status, prefs.sort]);

  const filtersActive = query.trim() !== "" || prefs.status !== "all";
  const clearFilters = () => {
    setQuery("");
    setPrefs({ status: "all" });
  };

  const actionsFor = (e: Entry): ProjectCardActions => ({
    canManage,
    canStop: canManage,
    powerPending: stopProject.isPending || startProject.isPending,
    knownFolders,
    folder: e.folder,
    onMoveToFolder: (folder) => setPref.mutate({ project: e.model.name, starred: e.starred, folder }),
    onStart: () => startProject.mutate(e.model.name),
    onRequestStop: () => setConfirmStop({ name: e.model.name, count: e.model.allServiceCount }),
  });

  const contextItems = (e: Entry): ContextMenuEntry[] => {
    const m = e.model;
    const items: ContextMenuEntry[] = [
      {
        id: "header",
        kind: "header",
        label: m.name,
        subtitle: `${m.serviceCount} service${m.serviceCount === 1 ? "" : "s"}`,
        icon: Box,
      },
      { id: "open", label: "Open project", icon: ArrowUpRight, onSelect: () => router.push(`/projects/${m.name}`) },
    ];
    if (m.publicURL) {
      const url = m.publicURL;
      items.push({ id: "open-url", label: "Visit site", icon: ExternalLink, onSelect: () => window.open(url, "_blank", "noopener") });
    }
    items.push({ id: "sep1", kind: "separator" });
    items.push({
      id: "star",
      label: e.starred ? "Unstar" : "Star",
      icon: Star,
      onSelect: () => setPref.mutate({ project: m.name, starred: !e.starred, folder: e.folder }),
    });
    if (canManage && m.allServiceCount > 0) {
      items.push(
        m.status === "stopped"
          ? { id: "start", label: "Start project", icon: Power, onSelect: () => startProject.mutate(m.name) }
          : {
              id: "stop",
              label: "Stop project…",
              icon: Pause,
              destructive: true,
              onSelect: () => setConfirmStop({ name: m.name, count: m.allServiceCount }),
            },
      );
    }
    if (canManage) {
      items.push({ id: "sep2", kind: "separator" });
      items.push({ id: "settings", label: "Project settings", icon: Settings, onSelect: () => router.push(`/projects/${m.name}/settings`) });
    }
    return items;
  };

  const renderGrid = (items: ReadonlyArray<Entry>) => (
    <ul className="grid items-stretch gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {items.map((e) => (
        <ProjectCard
          key={e.model.name}
          model={e.model}
          starred={e.starred}
          onToggleStar={() => setPref.mutate({ project: e.model.name, starred: !e.starred, folder: e.folder })}
          actions={actionsFor(e)}
          onContextMenu={(ev) => {
            ev.preventDefault();
            setCardMenu({ x: ev.clientX, y: ev.clientY, items: contextItems(e) });
          }}
        />
      ))}
    </ul>
  );

  // Starred pins to the top (only there, not also in its folder), then
  // folders alphabetically, then everything unfiled.
  const starred = visible.filter((e) => e.starred);
  const rest = visible.filter((e) => !e.starred);
  const folderNames = Array.from(new Set(rest.map((e) => e.folder).filter((f) => f !== ""))).sort((a, b) =>
    a.localeCompare(b),
  );
  const unfiled = rest.filter((e) => e.folder === "");
  const hasSections = starred.length > 0 || folderNames.length > 0;

  return (
    <>
      <ProjectsToolbar
        query={query}
        onQuery={setQuery}
        status={prefs.status}
        onStatus={(status) => setPrefs({ status })}
        counts={counts}
        sort={prefs.sort}
        onSort={(sort) => setPrefs({ sort })}
      />

      {visible.length === 0 ? (
        <div className="flex flex-col items-center rounded-lg border border-dashed border-[var(--border-subtle)] px-6 py-14 text-center">
          <SearchX className="h-6 w-6 text-[var(--text-tertiary)]" aria-hidden />
          <p className="mt-3 text-sm font-medium text-[var(--text-primary)]">No projects match</p>
          <p className="mt-1 max-w-sm text-[13px] text-[var(--text-secondary)]">
            {query.trim() ? (
              <>
                Nothing matches <span className="font-mono">{query.trim()}</span>
                {prefs.status !== "all" ? " with this status filter" : ""}.
              </>
            ) : (
              "No project is in this state right now."
            )}
          </p>
          {filtersActive && (
            <button
              type="button"
              onClick={clearFilters}
              className="mt-4 inline-flex h-8 items-center rounded-md border border-[var(--border-subtle)] px-3 text-xs font-medium text-[var(--text-primary)] outline-none hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
            >
              Clear filters
            </button>
          )}
        </div>
      ) : !hasSections ? (
        renderGrid(visible)
      ) : (
        <div className="space-y-7">
          {starred.length > 0 && (
            <Section icon={<Star className="h-3.5 w-3.5 fill-current text-[var(--warning)]" />} label="Starred" count={starred.length}>
              {renderGrid(starred)}
            </Section>
          )}
          {folderNames.map((f) => {
            const items = rest.filter((e) => e.folder === f);
            return (
              <Section key={f} icon={<Folder className="h-3.5 w-3.5" />} label={f} count={items.length}>
                {renderGrid(items)}
              </Section>
            );
          })}
          {unfiled.length > 0 && (
            <Section label="Other projects" count={unfiled.length} muted>
              {renderGrid(unfiled)}
            </Section>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirmStop !== null}
        title="Stop project"
        destructive
        confirmLabel="Stop project"
        typeToConfirm={confirmStop?.name}
        pending={stopProject.isPending}
        body={
          confirmStop ? (
            <p>
              This stops all {confirmStop.count} service{confirmStop.count === 1 ? "" : "s"} in{" "}
              <span className="font-mono text-[var(--text-primary)]">{confirmStop.name}</span>. Visitors get a 503
              until you start it again.
            </p>
          ) : null
        }
        onConfirm={() => {
          if (confirmStop) stopProject.mutate(confirmStop.name);
          setConfirmStop(null);
        }}
        onCancel={() => setConfirmStop(null)}
      />
      <CanvasContextMenu
        open={cardMenu !== null}
        x={cardMenu?.x ?? 0}
        y={cardMenu?.y ?? 0}
        items={cardMenu?.items ?? []}
        onClose={() => setCardMenu(null)}
      />
    </>
  );
}

function Section({
  icon,
  label,
  count,
  muted,
  children,
}: {
  icon?: React.ReactNode;
  label: string;
  count: number;
  muted?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(true);
  return (
    <section>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className={cn(
          "mb-2.5 inline-flex items-center gap-1.5 rounded-sm text-[13px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
          muted ? "text-[var(--text-secondary)]" : "text-[var(--text-primary)]",
        )}
      >
        <ChevronDown
          aria-hidden
          className={cn("h-3.5 w-3.5 text-[var(--text-tertiary)] motion-safe:transition-transform", !open && "-rotate-90")}
        />
        {icon}
        <span>{label}</span>
        <span className="tabular-nums text-[var(--text-tertiary)]">{count}</span>
      </button>
      {open && children}
    </section>
  );
}
