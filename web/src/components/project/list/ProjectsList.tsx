"use client";

import { useRovingGrid } from "@/lib/use-roving-grid";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowUpRight, Box, ChevronDown, ExternalLink, Folder, Pause, Power, SearchX, Settings, Star } from "lucide-react";
import {
  useProjectsSummary,
  useStartProject,
  useStopProject,
  type ProjectSummaryItem,
} from "@/features/projects";
import { useCan, Perms } from "@/features/auth";
import { ERRORS_UNAVAILABLE, useFleetErrorSummaries } from "@/features/errors";
import { Callout } from "@/components/ui/callout";
import { useProjectPrefs, useSetProjectPref } from "@/features/userprefs";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { CanvasContextMenu, type ContextMenuEntry } from "@/components/canvas/CanvasContextMenu";
import { cn } from "@/lib/utils";
import type { KusoProject } from "@/types/projects";
import { AttentionList, FleetPulse } from "./FleetPulse";
import { attentionItems, fleetCells, headline, memoryFootprint, projectErrorTotal, recentDeploys } from "./fleetModel";
import { BOARD_COLS, ProjectRow } from "./ProjectRow";
import type { ProjectCardActions } from "./ProjectCardMenu";
import { ProjectsToolbar } from "./ProjectsToolbar";
import {
  buildCardModel,
  collapsedSummary,
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

const COLLAPSED_KEY = "kuso.projects.collapsed";

// Folded groups survive a reload (per browser). Read after mount so the
// static prerender and first client render agree.
function useCollapsedSections(): [ReadonlySet<string>, (key: string) => void] {
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  useEffect(() => {
    try {
      const v: unknown = JSON.parse(window.localStorage.getItem(COLLAPSED_KEY) ?? "[]");
      if (Array.isArray(v)) setCollapsed(new Set(v.filter((x): x is string => typeof x === "string")));
    } catch {
      // Storage blocked or malformed: everything open.
    }
  }, []);
  const toggle = useCallback((key: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      try {
        window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...next]));
      } catch {
        // Not persisted; still toggles for this visit.
      }
      return next;
    });
  }, []);
  return [collapsed, toggle];
}

const EMPTY_COUNTS: Record<StatusFilter, number> = { all: 0, live: 0, failing: 0, building: 0, sleeping: 0, stopped: 0 };

// ProjectsList renders the toolbar + grouped grid. Every card's health
// comes from ONE batched /api/projects/summary poll (not a per-card
// fan-out).
export function ProjectsList({
  projects,
  header,
}: {
  projects: ReadonlyArray<KusoProject>;
  // The page header, given the one-line fleet summary as its subtitle.
  header: (subtitle: string) => React.ReactNode;
}) {
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
  const [collapsed, toggleSection] = useCollapsedSections();

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

  const names = useMemo(() => projects.map((p) => p.metadata.name), [projects]);
  const fleetErrors = useFleetErrorSummaries(names);
  const errorsUnavailable = fleetErrors.unavailable;

  const fleet = useMemo(() => {
    const models = entries.map((e) => e.model);
    let errors: number | undefined;
    let top: { name: string; count: number } | undefined;
    let maxErrors = 0;
    for (const m of models) {
      const n = projectErrorTotal(fleetErrors.byProject.get(m.name));
      if (n === undefined) continue;
      errors = (errors ?? 0) + n;
      if (n > maxErrors) {
        maxErrors = n;
        top = { name: m.name, count: n };
      }
    }
    const attention = attentionItems(models);
    const cells = fleetCells([...models].sort((a, b) => a.name.localeCompare(b.name)));
    return {
      cells,
      attention,
      errors,
      top,
      maxErrors,
      maxMem: Math.max(0, ...models.map((m) => m.metrics?.memBytes ?? 0)),
      footprint: memoryFootprint(models, 5),
      deploys: recentDeploys(summaryQuery.data ?? [], 4),
      headline: headline({ projects: models.length, services: cells.length, attention, errors }),
    };
  }, [entries, fleetErrors.byProject, summaryQuery.data]);

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

  const renderGrid = (items: ReadonlyArray<Entry>, header = true) => (
    <CardGrid header={header}>
      {items.map((e) => (
        <ProjectRow
          key={e.model.name}
          model={e.model}
          errors={fleetErrors.byProject.get(e.model.name)}
          maxErrors={fleet.maxErrors}
          maxMem={fleet.maxMem}
          starred={e.starred}
          onToggleStar={() => setPref.mutate({ project: e.model.name, starred: !e.starred, folder: e.folder })}
          actions={actionsFor(e)}
          onContextMenu={(ev) => {
            ev.preventDefault();
            setCardMenu({ x: ev.clientX, y: ev.clientY, items: contextItems(e) });
          }}
        />
      ))}
    </CardGrid>
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
      {header(summaryQuery.data ? fleet.headline : `${projects.length} project${projects.length === 1 ? "" : "s"}`)}
      {summaryQuery.data && (
        <>
          <FleetPulse
            cells={fleet.cells}
            errors={fleet.errors}
            topErrorProject={fleet.top}
            footprint={fleet.footprint}
            deploys={fleet.deploys}
          />
          <AttentionList items={fleet.attention} />
        </>
      )}
      <ProjectsToolbar
        query={query}
        onQuery={setQuery}
        status={prefs.status}
        onStatus={(status) => setPrefs({ status })}
        counts={counts}
        sort={prefs.sort}
        onSort={(sort) => setPrefs({ sort })}
      />

      {errorsUnavailable && (
        <Callout tone="neutral" className="mb-5 py-2 text-[13px] text-[var(--text-secondary)]">
          {ERRORS_UNAVAILABLE}, so cards show no error counts. That doesn&apos;t mean no errors; upgrade kuso to count them.
        </Callout>
      )}

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
            <Section
              icon={<Star className="h-3.5 w-3.5 fill-current text-[var(--warning)]" />}
              label="Starred"
              items={starred}
              open={!collapsed.has("starred")}
              onToggle={() => toggleSection("starred")}
            >
              {renderGrid(starred)}
            </Section>
          )}
          {folderNames.map((f) => {
            const items = rest.filter((e) => e.folder === f);
            return (
              <Section
                key={f}
                icon={<Folder className="h-3.5 w-3.5" />}
                label={f}
                items={items}
                open={!collapsed.has(`folder:${f}`)}
                onToggle={() => toggleSection(`folder:${f}`)}
              >
                {renderGrid(items, starred.length === 0 && f === folderNames[0])}
              </Section>
            );
          })}
          {unfiled.length > 0 && (
            <Section
              label="Other projects"
              items={unfiled}
              muted
              open={!collapsed.has("unfiled")}
              onToggle={() => toggleSection("unfiled")}
            >
              {renderGrid(unfiled, starred.length === 0 && folderNames.length === 0)}
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
              <span className="font-mono text-[var(--text-primary)]">{confirmStop.name}</span>. Public services return a 503 and workers stop
              processing until you start it again.
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
  items,
  muted,
  open,
  onToggle,
  children,
}: {
  icon?: React.ReactNode;
  label: string;
  items: ReadonlyArray<Entry>;
  muted?: boolean;
  open: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  const names = items.map((e) => e.model.name);
  const shown = names.slice(0, 4).join(", ");
  const summary = collapsedSummary(items.map((e) => e.model));
  return (
    <section>
      <button
        type="button"
        onClick={onToggle}
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
        <span className="tabular-nums text-[var(--text-tertiary)]">{items.length}</span>
      </button>
      {open ? (
        children
      ) : (
        // Folded: still say what's inside, and the row itself unfolds it.
        <button
          type="button"
          onClick={onToggle}
          aria-label={`Show ${items.length} project${items.length === 1 ? "" : "s"} in ${label}: ${summary}`}
          className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-dashed border-[var(--border-default)] px-4 py-2.5 text-left text-xs outline-none hover:border-[var(--border-strong)] hover:bg-[var(--bg-secondary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
        >
          <span className="min-w-0 flex-1 truncate text-[var(--text-secondary)]">
            {shown}
            {names.length > 4 && <span className="text-[var(--text-tertiary)]"> +{names.length - 4} more</span>}
          </span>
          {summary && <span className="shrink-0 tabular-nums text-[var(--text-tertiary)]">{summary}</span>}
        </button>
      )}
    </section>
  );
}

// One tab stop per board: arrows move between rows (see useRovingGrid).
function CardGrid({ children, header }: { children: React.ReactNode; header: boolean }) {
  const ref = useRovingGrid<HTMLUListElement>();
  return (
    <div className="overflow-hidden rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] shadow-node">
      <div
        aria-hidden
        className={cn(
          !header && "lg:!hidden",
          "hidden border-b border-[var(--border-subtle)] px-4 py-2 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--text-tertiary)]",
          BOARD_COLS,
        )}
      >
        <span>Project</span>
        <span>Services</span>
        <span className="text-right">Errors · 24h</span>
        <span className="text-right">CPU · Memory</span>
        <span className="text-right">Deployed</span>
        <span />
      </div>
      <ul ref={ref} className="divide-y divide-[var(--border-subtle)]">
        {children}
      </ul>
    </div>
  );
}
