"use client";

import {
  Children,
  createContext,
  Fragment,
  isValidElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import { useRouter, usePathname } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTheme } from "next-themes";
import { Command } from "cmdk";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import {
  ArrowLeft,
  ChevronRight,
  Clock,
  Copy,
  CornerDownLeft,
  Database,
  ExternalLink,
  GitBranch,
  History,
  LayoutGrid,
  LogOut,
  Moon,
  Play,
  Plus,
  RotateCw,
  ScrollText,
  Search,
  Server,
  Settings,
  Activity,
  Star,
  Store,
  Sun,
  TriangleAlert,
  Variable,
  Shield,
  Undo2,
} from "lucide-react";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { Kbd } from "@/components/ui/kbd";
import { isMac } from "@/lib/shortcuts";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { getProjectsSummary, projectsSummaryQueryKey, useAddons } from "@/features/projects";
import { useLiveUrl } from "@/features/projects/liveDomain";
import { useProjectPrefs } from "@/features/userprefs";
import { useSession, useSignOut } from "@/features/auth";
import { buildTriggerMessage, buildsQueryKey, restartService, triggerBuild, useBuilds } from "@/features/services";
import { buildNeverReady, buildRefLabel, buildTitle, liveBuildId, previousDeployTarget, rollbackToBuild } from "@/features/builds";
import { useEnvironments } from "@/features/projects/hooks";
import { liveImagesOf } from "@/features/revisions";
import { envGroupName } from "@/lib/env-group";
import type { KusoAddon } from "@/types/projects";
import { cn } from "@/lib/utils";
import { TONE_CLASSES } from "@/lib/status";
import {
  buildPaletteIndex,
  canRedeploy,
  envVarHref,
  matchScore,
  loadIndexSnapshot,
  loadRecents,
  pathLabel,
  pushRecent,
  rankScore,
  recentKey,
  saveIndexSnapshot,
  saveRecents,
  serviceActions,
  soleActionTarget,
  serviceHref,
  shouldGroupByProject,
  tierBoost,
  type PaletteIndex,
  type PaletteProject,
  type PaletteService,
  type PaletteStatus,
  type RankContext,
  type RecentItem,
  type ServiceActionKind,
} from "./paletteIndex";
import { SETTINGS_NAV } from "@/components/settings/nav";
import { addonPageHref, servicePageHref } from "@/components/service/workspace/hrefs";

const ACTION_ICON: Record<ServiceActionKind, ReactNode> = {
  logs: <ScrollText />,
  deployments: <History />,
  variables: <Variable />,
  metrics: <Activity />,
  settings: <Settings />,
  redeploy: <Play />,
  rollback: <Undo2 />,
  restart: <RotateCw />,
};

// Nested pages, Railway-style: Enter on a project or service drills in,
// Backspace on an empty query (or Esc) goes back up.
type Page =
  | { kind: "project"; project: string }
  | { kind: "service"; project: string; name: string; group: string };

type PendingAction = { kind: "redeploy" | "restart" | "rollback"; svc: PaletteService };

function currentProjectFromPath(pathname: string | null): string {
  if (!pathname) return "";
  const m = pathname.match(/^\/projects\/([^/?]+)/);
  // /projects/new is the create page, not a project called "new".
  return m && m[1] !== "new" ? decodeURIComponent(m[1]) : "";
}

const STATUS_STYLE: Record<PaletteStatus, { dot: string; label: string }> = {
  live: { dot: TONE_CLASSES.success.dot, label: "Live" },
  deploying: { dot: `${TONE_CLASSES.progress.dot} motion-safe:animate-pulse`, label: "Deploying" },
  failing: { dot: TONE_CLASSES.danger.dot, label: "Failing" },
  sleeping: { dot: "bg-[var(--text-tertiary)]", label: "Sleeping" },
  stopped: { dot: "border border-[var(--text-tertiary)]", label: "Stopped" },
  idle: { dot: "border border-[var(--text-tertiary)]", label: "Not deployed" },
};

function StatusBadge({ status, text }: { status: PaletteStatus; text?: string }) {
  const s = STATUS_STYLE[status];
  return (
    <span className="ml-auto inline-flex shrink-0 items-center gap-1.5 text-xs text-[var(--text-tertiary)]">
      <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", s.dot)} />
      {text ?? s.label}
    </span>
  );
}

// "tickero / api", with the project muted so the target reads first.
function PathLabel({ project, name }: { project: string; name: ReactNode }) {
  return (
    <>
      <span className="text-[var(--text-secondary)]">{project}</span>
      <span className="text-[var(--text-tertiary)]"> / </span>
      {name}
    </>
  );
}

const ID_SEP = "::";

// The palette ranks rows itself (cmdk's built-in fuzzy filter matched
// "web" against "worker" and couldn't be told to rank groups), so each
// Group reads the live query from context, drops rows that don't match
// and orders the rest by matchScore.
const QueryContext = createContext("");

// Rows that drill into a submenu register here under their cmdk value,
// so → / Tab on the selected row can drill while Enter opens it.
type DrillRegistry = Map<string, () => void>;
const DrillContext = createContext<DrillRegistry | null>(null);

type RowElement = ReactElement<RowProps>;

function flattenRows(children: ReactNode): RowElement[] {
  const out: RowElement[] = [];
  Children.forEach(children, (child) => {
    if (!isValidElement(child)) return;
    if (child.type === Fragment) {
      out.push(...flattenRows((child.props as { children?: ReactNode }).children));
    } else if (child.type === Row) {
      out.push(child as RowElement);
    }
  });
  return out;
}

function rankRows(children: ReactNode, query: string): { row: RowElement; score: number }[] {
  const rows = flattenRows(children);
  if (!query) return rows.map((row) => ({ row, score: 1 }));
  return rows
    .map((row) => {
      return { row, score: rankScore(matchScore(query, row.props.value, row.props.keywords), row.props.boost ?? 0) };
    })
    .filter((r) => r.score > 0)
    .sort((a, b) => b.score - a.score);
}

const groupClass =
  "px-1.5 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:pt-2.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-[var(--text-tertiary)]";

interface GroupProps {
  heading: string;
  // Caps how many matches one group shows, so a broad query over
  // hundreds of variables doesn't bury everything else.
  limit?: number;
  children: ReactNode;
}

function Group({ heading, limit, children }: GroupProps) {
  const query = useContext(QueryContext);
  const ranked = rankRows(children, query);
  if (ranked.length === 0) return null;
  return (
    <Command.Group heading={heading} className={groupClass}>
      {(limit ? ranked.slice(0, limit) : ranked).map((r) => (
        <Fragment key={`${r.row.props.id ?? ""}${ID_SEP}${r.row.props.value}`}>{r.row}</Fragment>
      ))}
    </Command.Group>
  );
}

// Orders sibling Groups by their best match while searching.
function RankedGroups({ children }: { children: ReactNode }) {
  const query = useContext(QueryContext);
  const groups: ReactElement<GroupProps>[] = [];
  const collect = (nodes: ReactNode) =>
    Children.forEach(nodes, (child) => {
      if (!isValidElement(child)) return;
      if (child.type === Fragment) collect((child.props as { children?: ReactNode }).children);
      else if (child.type === Group) groups.push(child as ReactElement<GroupProps>);
    });
  collect(children);
  if (!query) return <>{groups.map((g) => <Fragment key={g.props.heading}>{g}</Fragment>)}</>;
  return (
    <>
      {groups
        .map((g, i) => ({ g, i, best: rankRows(g.props.children, query)[0]?.score ?? 0 }))
        .filter((x) => x.best > 0)
        .sort((a, b) => b.best - a.best || a.i - b.i)
        .map((x) => (
          <Fragment key={x.g.props.heading}>{x.g}</Fragment>
        ))}
    </>
  );
}

interface RowProps {
  // Disambiguates rows that show the same entity in several groups.
  id?: string;
  // What the query is matched against (shown text + context).
  value: string;
  keywords?: string[];
  onSelect: () => void;
  icon: ReactNode;
  children: ReactNode;
  hint?: ReactNode;
  right?: ReactNode;
  // Opens this row's submenu (→ or Tab); Enter still runs onSelect.
  onDrill?: () => void;
  // Tier boost from tierBoost(): current project > recent > starred.
  boost?: number;
}

function Row({
  id,
  value,
  keywords,
  onSelect,
  icon,
  children,
  hint,
  right,
  onDrill,
}: RowProps) {
  const itemValue = id ? `${id}${ID_SEP}${value}` : value;
  const drills = useContext(DrillContext);
  useEffect(() => {
    if (!drills || !onDrill) return;
    drills.set(itemValue, onDrill);
    return () => {
      if (drills.get(itemValue) === onDrill) drills.delete(itemValue);
    };
  }, [drills, itemValue, onDrill]);
  return (
    <Command.Item
      value={itemValue}
      keywords={keywords}
      onSelect={onSelect}
      className="group relative flex min-h-10 cursor-pointer select-none items-center gap-3 rounded-md px-2.5 py-2 text-sm text-[var(--text-secondary)] outline-none before:absolute before:inset-y-1.5 before:left-0 before:w-[3px] before:rounded-full before:bg-transparent data-[selected=true]:bg-[var(--selected)] data-[selected=true]:text-[var(--text-primary)] data-[selected=true]:before:bg-[var(--accent)]"
    >
      <span className="flex h-4 w-4 shrink-0 items-center justify-center text-[var(--text-tertiary)] group-data-[selected=true]:text-[var(--text-primary)] [&_svg]:h-4 [&_svg]:w-4">
        {icon}
      </span>
      <span className="flex min-w-0 flex-1 items-baseline gap-2">
        <span className="truncate text-[var(--text-primary)]">{children}</span>
        {hint && <span className="truncate text-xs text-[var(--text-tertiary)]">{hint}</span>}
      </span>
      {right}
      {onDrill && (
        <span className="hidden shrink-0 items-center gap-1 text-xs text-[var(--text-tertiary)] group-data-[selected=true]:inline-flex">
          Actions <Kbd>→</Kbd>
        </span>
      )}
      {onDrill && <ChevronRight aria-hidden className="h-4 w-4 shrink-0 text-[var(--text-tertiary)] group-data-[selected=true]:hidden" />}
    </Command.Item>
  );
}

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [pages, setPages] = useState<Page[]>([]);
  const [recents, setRecents] = useState<RecentItem[]>([]);
  const [snapshot, setSnapshot] = useState<PaletteIndex | null>(null);
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [selected, setSelected] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [drills] = useState<DrillRegistry>(() => new Map());
  const router = useRouter();
  const pathname = usePathname();
  const qc = useQueryClient();
  const { data: session } = useSession();
  const signOut = useSignOut();
  const { resolvedTheme, setTheme } = useTheme();
  const currentProject = useMemo(() => currentProjectFromPath(pathname), [pathname]);
  const page = pages[pages.length - 1];
  const pageProject = page?.project ?? "";

  // Same query key as the projects dashboard, so an open palette reuses
  // its cache. One request indexes every project/service/env; nothing
  // fetches until the palette is first opened.
  const summary = useQuery({
    queryKey: projectsSummaryQueryKey,
    queryFn: getProjectsSummary,
    enabled: open,
    staleTime: 25_000,
  });
  const prefs = useProjectPrefs();
  // Addons aren't in the summary: load them for the project in context
  // only, and fold in any other project's addons already cached.
  const ctxProject = pageProject || currentProject;
  const ctxAddons = useAddons(open ? ctxProject : "");

  // Live summary when we have it; otherwise the last index this browser
  // built, so the palette never opens on a loading line.
  const liveIndex = useMemo(() => (summary.data ? buildPaletteIndex(summary.data) : null), [summary.data]);
  const index = useMemo<PaletteIndex>(() => liveIndex ?? snapshot ?? { projects: [], services: [], envs: [] }, [liveIndex, snapshot]);
  const userId = session?.user.id ?? "";
  useEffect(() => {
    if (liveIndex) saveIndexSnapshot(liveIndex, userId);
  }, [liveIndex, userId]);
  const projectByName = useMemo(() => new Map(index.projects.map((p) => [p.name, p])), [index.projects]);
  const servicesByProject = useMemo(() => {
    const m = new Map<string, PaletteService[]>();
    for (const s of index.services) m.set(s.project, [...(m.get(s.project) ?? []), s]);
    return m;
  }, [index.services]);

  const actions = useMemo(() => serviceActions(index.services), [index.services]);

  const addons = useMemo(() => {
    if (!open) return [] as { project: string; addon: KusoAddon }[];
    const out: { project: string; addon: KusoAddon }[] = [];
    const seen = new Set<string>();
    for (const a of ctxAddons.data ?? []) {
      seen.add(`${ctxProject}/${a.metadata.name}`);
      out.push({ project: ctxProject, addon: a });
    }
    const cached = qc.getQueriesData<KusoAddon[]>({
      predicate: (q) => q.queryKey[0] === "projects" && q.queryKey[2] === "addons" && q.queryKey.length === 3,
    });
    for (const [key, data] of cached) {
      const project = String(key[1]);
      for (const a of data ?? []) {
        const id = `${project}/${a.metadata.name}`;
        if (seen.has(id)) continue;
        seen.add(id);
        out.push({ project, addon: a });
      }
    }
    return out;
  }, [open, qc, ctxProject, ctxAddons.data]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.key === "k" || e.key === "K") && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((v) => !v);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    setSnapshot(loadIndexSnapshot(userId));
  }, [userId]);

  // Re-read on open: another tab may have added recents since.
  useEffect(() => {
    setRecents(loadRecents());
  }, [open]);

  // Storage is the source of truth, so a write before the first load (or
  // from another tab) never clobbers the list.
  const remember = useCallback((item: RecentItem) => {
    const next = pushRecent(loadRecents(), item);
    saveRecents(next);
    setRecents(next);
  }, []);

  // Opening a project any way (sidebar, link, URL) counts as recent.
  useEffect(() => {
    if (currentProject) remember({ kind: "project", project: currentProject });
  }, [currentProject, remember]);

  const close = useCallback(() => {
    setOpen(false);
    setSearch("");
    setPages([]);
  }, []);

  const go = useCallback(
    (path: string, recent?: RecentItem) => {
      if (recent) remember(recent);
      close();
      router.push(path);
    },
    [close, remember, router],
  );

  const pushPage = (p: Page) => {
    setPages((prev) => [...prev, p]);
    setSearch("");
    inputRef.current?.focus();
  };
  const popPage = () => {
    setPages((prev) => prev.slice(0, -1));
    setSearch("");
  };

  const perms = session?.session.permissions ?? [];

  const starred = useMemo(
    () =>
      index.projects
        .filter((p) => prefs.byProject.get(p.name)?.starred)
        .sort((a, b) => a.name.localeCompare(b.name)),
    [index.projects, prefs.byProject],
  );

  const rankCtx = useMemo<RankContext>(
    () => ({ currentProject, recents, starred: new Set(starred.map((p) => p.name)) }),
    [currentProject, recents, starred],
  );
  const svcKey = (s: PaletteService) => recentKey({ kind: "service", project: s.project, name: s.name, group: s.group });
  const svcBoost = (s: PaletteService) => tierBoost(rankCtx, { project: s.project, key: svcKey(s) });

  const recentRows = useMemo(() => {
    return recents
      .map((r) => {
        if (r.kind === "project") {
          if (r.project === currentProject) return null;
          const p = projectByName.get(r.project);
          return p ? { r, project: p } : null;
        }
        if (r.kind === "service") {
          const s = (servicesByProject.get(r.project) ?? []).find((x) => x.name === r.name && x.group === r.group);
          return s ? { r, service: s } : null;
        }
        return { r, addon: r.name };
      })
      .filter((x): x is NonNullable<typeof x> => x !== null)
      .slice(0, 6);
  }, [recents, projectByName, servicesByProject, currentProject]);

  const runAction = async ({ kind, svc }: PendingAction) => {
    setPending(null);
    try {
      if (kind === "redeploy") {
        const res = await triggerBuild(svc.project, svc.name, svc.branch ? { branch: svc.branch } : {});
        toast.success(buildTriggerMessage(res, `Redeploy started for ${svc.display}`, svc.name));
      } else {
        await restartService(svc.project, svc.name, svc.group);
        toast.success(`Restarting ${svc.display} (${svc.group})`);
      }
    } catch (err) {
      toastError(err, `Failed to ${kind} ${svc.display}`);
    }
  };

  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(`${what} copied`);
    } catch {
      toast.error("Couldn't copy to the clipboard");
    }
    close();
  };

  const projectRow = (p: PaletteProject, prefix: string) => (
    <Row
      key={`${prefix}-${p.name}`}
      id={`${prefix}-p-${p.name}`}
      value={p.name}
      keywords={[p.description ?? ""]}
      boost={tierBoost(rankCtx, { project: p.name, key: `p:${p.name}` })}
      onSelect={() => go(`/projects/${encodeURIComponent(p.name)}`, { kind: "project", project: p.name })}
      onDrill={() => {
        remember({ kind: "project", project: p.name });
        pushPage({ kind: "project", project: p.name });
      }}
      icon={<LayoutGrid />}
      hint={p.description}
      right={
        <StatusBadge
          status={p.status}
          text={p.services === 0 ? "Empty" : p.status === "live" ? `${p.live}/${p.services} live` : undefined}
        />
      }
    >
      {p.name}
    </Row>
  );

  const serviceRow = (s: PaletteService, prefix: string, showProject = true) => (
    <Row
      key={`${prefix}-${s.project}-${s.name}`}
      id={`${prefix}-s-${s.project}-${s.name}`}
      value={pathLabel(s.project, s.display)}
      keywords={[s.name, s.runtime ?? "", s.repo ?? ""]}
      boost={svcBoost(s)}
      onSelect={() => go(serviceHref(s), { kind: "service", project: s.project, name: s.name, group: s.group })}
      onDrill={() => {
        remember({ kind: "service", project: s.project, name: s.name, group: s.group });
        pushPage({ kind: "service", project: s.project, name: s.name, group: s.group });
      }}
      icon={<Server />}
      hint={showProject ? undefined : s.runtime}
      right={<StatusBadge status={s.status} />}
    >
      {showProject ? <PathLabel project={s.project} name={s.display} /> : s.display}
    </Row>
  );

  const addonRow = (project: string, a: KusoAddon, prefix: string, showProject = true) => (
    <Row
      key={`${prefix}-${project}-${a.metadata.name}`}
      id={`${prefix}-a-${project}-${a.metadata.name}`}
      value={pathLabel(project, a.metadata.name)}
      keywords={[a.spec.kind ?? "", "database"]}
      boost={tierBoost(rankCtx, { project, key: `a:${project}/${a.metadata.name}` })}
      onSelect={() =>
        go(addonPageHref(project, a.metadata.name), {
          kind: "addon",
          project,
          name: a.metadata.name,
        })
      }
      icon={<Database />}
      hint={a.spec.kind}
    >
      {showProject ? <PathLabel project={project} name={a.metadata.name} /> : a.metadata.name}
    </Row>
  );

  const query = search.trim();

  // We filter ourselves (shouldFilter=false), so cmdk doesn't re-pick the
  // first row when the results change. Do it here, and pin the list to
  // the top so a new query never opens scrolled halfway down.
  useEffect(() => {
    if (!open) return;
    const id = requestAnimationFrame(() => {
      const first = listRef.current?.querySelector("[cmdk-item]");
      setSelected(first?.getAttribute("data-value") ?? "");
      if (listRef.current) listRef.current.scrollTop = 0;
    });
    return () => cancelAnimationFrame(id);
  }, [open, query, pages.length, summary.data]);
  // Only when there's genuinely nothing to show yet (first ever open).
  const loadingIndex = open && !liveIndex && !snapshot && summary.isFetching;

  const pageSvc =
    page?.kind === "service"
      ? (servicesByProject.get(page.project) ?? []).find((s) => s.name === page.name && s.group === page.group)
      : undefined;
  const pageSvcUrl = useLiveUrl(pageSvc?.project ?? "", pageSvc?.name ?? "", pageSvc?.url, pageSvc?.urlFallback);

  let body: ReactNode;
  if (!page) {
    const ctxServices = currentProject ? (servicesByProject.get(currentProject) ?? []) : [];
    // The current project's services live in its "In <project>" group;
    // the global list covers everything else, split per project when
    // the query hits many services across several projects.
    const otherServices = index.services.filter((s) => s.project !== currentProject);
    const hits = query ? index.services.filter((s) => matchScore(query, pathLabel(s.project, s.display), [s.name, s.runtime ?? "", s.repo ?? ""]) > 0) : [];
    const groupServices = shouldGroupByProject(hits);
    const actionTarget = query ? soleActionTarget(actions, query) : null;
    const servicesByOtherProject = [...servicesByProject.entries()].filter(([proj]) => proj !== currentProject);
    body = (
      <>
        {!query && recentRows.length > 0 && (
          <Group heading="Recent">
            {recentRows.map((row) =>
              "project" in row && row.project
                ? projectRow(row.project, "recent")
                : "service" in row && row.service
                  ? serviceRow(row.service, "recent")
                  : (
                      <Row
                        key={`recent-${recentKey(row.r)}`}
                        id={`recent-${recentKey(row.r)}`}
                        value={pathLabel(row.r.project, row.addon ?? "")}
                        onSelect={() =>
                          go(addonPageHref(row.r.project, row.addon ?? ""), row.r)
                        }
                        icon={<Database />}
                      >
                        <PathLabel project={row.r.project} name={row.addon} />
                      </Row>
                    ),
            )}
          </Group>
        )}

        {currentProject && (
          <Group heading={`In ${currentProject}`}>
            {!query && projectByName.get(currentProject) && projectRow(projectByName.get(currentProject)!, "ctx")}
            {ctxServices.map((s) => serviceRow(s, "ctx", false))}
            <Row
              id="ctx" value="add service"
              keywords={["new", "create", "deploy", "repo"]}
              onSelect={() => go(`/projects/${encodeURIComponent(currentProject)}/services/new`)}
              icon={<Plus />}
            >
              Add service
            </Row>
            <Row
              id="ctx" value="add addon"
              keywords={["new", "create", "database", "postgres", "redis"]}
              onSelect={() => go(`/projects/${encodeURIComponent(currentProject)}?add=addon`)}
              icon={<Database />}
            >
              Add addon
            </Row>
            <Row
              id="ctx" value="add cron"
              keywords={["new", "create", "schedule", "job"]}
              onSelect={() => go(`/projects/${encodeURIComponent(currentProject)}?add=cron`)}
              icon={<Clock />}
            >
              Add cron job
            </Row>
          </Group>
        )}

        {!query && starred.length > 0 && (
          <Group heading="Starred">{starred.map((p) => projectRow(p, "starred"))}</Group>
        )}

        {query && actions.length > 0 && (
          <Group heading={actionTarget ? `Actions · ${pathLabel(actionTarget.project, actionTarget.display)}` : "Service actions"} limit={30}>
            {actions.map((a) => {
              const recent: RecentItem = { kind: "service", project: a.svc.project, name: a.svc.name, group: a.svc.group };
              const confirmKind = a.kind === "redeploy" || a.kind === "restart" || a.kind === "rollback" ? a.kind : null;
              return (
                <Row
                  key={`act-${a.kind}-${a.svc.project}-${a.svc.name}`}
                  id={`act-${a.kind}-${a.svc.project}-${a.svc.name}`}
                  value={a.label}
                  keywords={a.keywords}
                  boost={svcBoost(a.svc)}
                  onSelect={() => {
                    if (confirmKind) {
                      remember(recent);
                      close();
                      setPending({ kind: confirmKind, svc: a.svc });
                    } else {
                      go(serviceHref(a.svc, a.tab), recent);
                    }
                  }}
                  icon={ACTION_ICON[a.kind]}
                  right={<StatusBadge status={a.svc.status} />}
                >
                  {a.verb}
                  {!actionTarget && (
                    <>
                      <span className="text-[var(--text-tertiary)]">: </span>
                      <PathLabel project={a.svc.project} name={a.svc.display} />
                    </>
                  )}
                </Row>
              );
            })}
          </Group>
        )}
        {query && (
          <Group heading="Project settings">
            {index.projects.map((p) => (
              <Row
                key={`act-psettings-${p.name}`}
                id={`act-psettings-${p.name}`}
                value={`Project settings: ${p.name}`}
                keywords={["config", "domain", "delete", "settings"]}
                boost={tierBoost(rankCtx, { project: p.name })}
                onSelect={() => go(`/projects/${encodeURIComponent(p.name)}/settings`, { kind: "project", project: p.name })}
                icon={<Settings />}
              >
                Project settings<span className="text-[var(--text-tertiary)]">: </span>
                {p.name}
              </Row>
            ))}
          </Group>
        )}

        {query && (
          <>
            {index.projects.length > 0 && (
              <Group heading="Projects">{index.projects.map((p) => projectRow(p, "all"))}</Group>
            )}
            {otherServices.length > 0 &&
              (groupServices ? (
                servicesByOtherProject.map(([proj, list]) => (
                  <Group key={proj} heading={`In ${proj}`} limit={8}>
                    {list.map((s) => serviceRow(s, "all", false))}
                  </Group>
                ))
              ) : (
                <Group heading="Services" limit={25}>{otherServices.map((s) => serviceRow(s, "all"))}</Group>
              ))}
            {addons.length > 0 && (
              <Group heading="Addons">{addons.map(({ project, addon }) => addonRow(project, addon, "all"))}</Group>
            )}
            {index.envs.length > 0 && (
              <Group heading="Environments">
                {index.envs.map((e) => (
                  <Row
                    key={`env-${e.project}-${e.group}`}
                    id={`env-${e.project}`}
                    value={pathLabel(e.project, e.group)}
                    keywords={["environment", e.kind]}
                    boost={tierBoost(rankCtx, { project: e.project })}
                    onSelect={() => go(`/projects/${encodeURIComponent(e.project)}?env=${encodeURIComponent(e.group)}`, { kind: "project", project: e.project })}
                    icon={<GitBranch />}
                    right={<span className="ml-auto text-xs text-[var(--text-tertiary)]">{e.kind === "preview" ? "Preview" : "Environment"}</span>}
                  >
                    <PathLabel project={e.project} name={e.group} />
                  </Row>
                ))}
              </Group>
            )}
            {query.length >= 2 && (
              <Group heading="Variables" limit={30}>
                {index.services.flatMap((s) =>
                  s.envVarKeys.map((key) => (
                    <Row
                      key={`var-${s.project}-${s.name}-${key}`}
                      id={`var-${s.project}-${s.name}`}
                      value={key}
                      keywords={[s.display, s.project, "variable"]}
                      boost={svcBoost(s)}
                      onSelect={() => go(envVarHref(s, key), { kind: "service", project: s.project, name: s.name, group: s.group })}
                      icon={<Variable />}
                      hint={pathLabel(s.project, s.display)}
                    >
                      <span className="font-mono text-[13px]">{key}</span>
                    </Row>
                  )),
                )}
              </Group>
            )}
          </>
        )}

        <Group heading="Create">
          <Row value="new project" keywords={["create", "add", "repo", "deploy"]} onSelect={() => go("/projects/new")} icon={<Plus />}>
            New project
          </Row>
          <Row value="marketplace templates" keywords={["template", "app", "one-click", "deploy"]} onSelect={() => go("/marketplace")} icon={<Store />}>
            Deploy a template
          </Row>
        </Group>

        <Group heading="Go to">
          <Row value="go all projects" keywords={["home", "dashboard", "list"]} onSelect={() => go("/projects")} icon={<LayoutGrid />}>
            All projects
          </Row>
          <Row value="go settings" keywords={["preferences", "config"]} onSelect={() => go("/settings")} icon={<Settings />}>
            Settings
          </Row>
          <Row value="go welcome" keywords={["getting started", "tour", "onboarding", "help"]} onSelect={() => go("/welcome")} icon={<Star />}>
            Getting started
          </Row>
        </Group>

        {query && (
          <Group heading="Settings pages">
            {SETTINGS_NAV.filter((item) => !item.perm || perms.includes(item.perm)).map((item) => {
              const Icon = item.icon;
              return (
                <Row
                  key={item.href}
                  id={`nav-${item.href}`}
                  value={`Settings: ${item.title}`}
                  keywords={[item.description, item.keywords ?? ""]}
                  onSelect={() => go(item.href)}
                  icon={<Icon />}
                  hint={item.description}
                >
                  Settings<span className="text-[var(--text-tertiary)]">: </span>
                  {item.title}
                </Row>
              );
            })}
          </Group>
        )}

        <Group heading="Preferences">
          <Row
            value="toggle theme"
            keywords={["dark", "light", "appearance"]}
            onSelect={() => {
              setTheme(resolvedTheme === "dark" ? "light" : "dark");
              close();
            }}
            icon={resolvedTheme === "dark" ? <Sun /> : <Moon />}
          >
            Switch to {resolvedTheme === "dark" ? "light" : "dark"} theme
          </Row>
          <Row
            value="open docs"
            keywords={["documentation", "help"]}
            onSelect={() => {
              close();
              window.open("https://github.com/sislelabs/kuso/blob/main/docs", "_blank", "noopener");
            }}
            icon={<ExternalLink />}
          >
            Documentation
          </Row>
          <Row
            value="sign out"
            keywords={["logout", "log out"]}
            onSelect={() => {
              close();
              signOut();
            }}
            icon={<LogOut />}
          >
            Sign out
          </Row>
        </Group>
      </>
    );
  } else if (page.kind === "project") {
    const p = page.project;
    const enc = encodeURIComponent(p);
    const svcs = servicesByProject.get(p) ?? [];
    const projAddons = addons.filter((a) => a.project === p);
    const envs = index.envs.filter((e) => e.project === p);
    body = (
      <>
        <Group heading={p}>
          <Row value="open project canvas" keywords={["go", "view"]} onSelect={() => go(`/projects/${enc}`, { kind: "project", project: p })} icon={<CornerDownLeft />}>
            Open project
          </Row>
        </Group>
        {svcs.length > 0 && <Group heading="Services">{svcs.map((s) => serviceRow(s, "proj", false))}</Group>}
        {projAddons.length > 0 && (
          <Group heading="Addons">{projAddons.map(({ addon }) => addonRow(p, addon, "proj", false))}</Group>
        )}
        <Group heading="Environments">
          <Row value="env production" keywords={["switch", "environment"]} onSelect={() => go(`/projects/${enc}`, { kind: "project", project: p })} icon={<GitBranch />}>
            production
          </Row>
          {envs.map((e) => (
            <Row
              key={`penv-${e.group}`}
              value={`env ${e.group}`}
              keywords={["switch", "environment", e.kind]}
              onSelect={() => go(`/projects/${enc}?env=${encodeURIComponent(e.group)}`, { kind: "project", project: p })}
              icon={<GitBranch />}
              right={<span className="ml-auto text-xs text-[var(--text-tertiary)]">{e.services} service{e.services === 1 ? "" : "s"}</span>}
            >
              {e.group}
            </Row>
          ))}
        </Group>
        <Group heading="Actions">
          <Row value="add service" keywords={["new", "create", "repo"]} onSelect={() => go(`/projects/${enc}/services/new`)} icon={<Plus />}>
            Add service
          </Row>
          <Row value="add addon" keywords={["new", "database", "postgres", "redis"]} onSelect={() => go(`/projects/${enc}?add=addon`)} icon={<Database />}>
            Add addon
          </Row>
          <Row value="add cron" keywords={["new", "schedule", "job"]} onSelect={() => go(`/projects/${enc}?add=cron`)} icon={<Clock />}>
            Add cron job
          </Row>
          <Row value="project settings" keywords={["config", "domain", "delete"]} onSelect={() => go(`/projects/${enc}/settings`)} icon={<Settings />}>
            Project settings
          </Row>
        </Group>
      </>
    );
  } else {
    const svc = (servicesByProject.get(page.project) ?? []).find((s) => s.name === page.name && s.group === page.group);
    const recent: RecentItem = { kind: "service", project: page.project, name: page.name, group: page.group };
    if (!svc) {
      body = (
        <Group heading={page.name}>
          <Row value="open service" onSelect={() => go(servicePageHref(page.project, page.name, { env: page.group }))} icon={<CornerDownLeft />}>
            Open service
          </Row>
        </Group>
      );
    } else {
      const tabs: { tab: string; label: string; icon: ReactNode; keywords: string[] }[] = [
        { tab: "deployments", label: "Deployments", icon: <History />, keywords: ["builds", "history", "rollback"] },
        { tab: "logs", label: "Logs", icon: <ScrollText />, keywords: ["tail", "output"] },
        { tab: "variables", label: "Variables", icon: <Variable />, keywords: ["env", "secrets", "config"] },
        { tab: "metrics", label: "Metrics", icon: <Activity />, keywords: ["cpu", "memory", "latency"] },
        { tab: "errors", label: "Errors", icon: <TriangleAlert />, keywords: ["exceptions", "crashes"] },
        { tab: "vulnerabilities", label: "Vulnerabilities", icon: <Shield />, keywords: ["cve", "security", "scan"] },
        { tab: "settings", label: "Settings", icon: <Settings />, keywords: ["domain", "scale", "delete", "port"] },
      ];
      body = (
        <>
          <Group heading={`${svc.display} · ${svc.project}`}>
            <Row value="open service" keywords={["go", "view"]} onSelect={() => go(serviceHref(svc), recent)} icon={<CornerDownLeft />} right={<StatusBadge status={svc.status} />}>
              Open service
            </Row>
            {tabs.map((t) => (
              <Row key={t.tab} value={`tab ${t.label}`} keywords={t.keywords} onSelect={() => go(serviceHref(svc, t.tab), recent)} icon={t.icon}>
                {t.label}
              </Row>
            ))}
          </Group>
          <Group heading="Actions">
            {canRedeploy(svc) && (
              <Row
                value="redeploy"
                keywords={["deploy", "build", "trigger", "latest"]}
                onSelect={() => {
                  close();
                  setPending({ kind: "redeploy", svc });
                }}
                icon={<Play />}
                hint={svc.branch ? `from ${svc.branch}` : "from the tracked branch"}
              >
                Redeploy latest commit
              </Row>
            )}
            {canRedeploy(svc) && (
              <Row
                value="rollback"
                keywords={["revert", "undo", "previous", "roll back"]}
                onSelect={() => {
                  close();
                  setPending({ kind: "rollback", svc });
                }}
                icon={<Undo2 />}
                hint={svc.group}
              >
                Roll back to previous build
              </Row>
            )}
            <Row
              value="restart"
              keywords={["reboot", "roll", "pods"]}
              onSelect={() => {
                close();
                setPending({ kind: "restart", svc });
              }}
              icon={<RotateCw />}
              hint={svc.group}
            >
              Restart
            </Row>
            {pageSvcUrl && (
              <>
                <Row value="open url" keywords={["visit", "browser", "domain"]} onSelect={() => { close(); window.open(pageSvcUrl, "_blank", "noopener"); }} icon={<ExternalLink />} hint={pageSvcUrl.replace(/^https?:\/\//, "")}>
                  Open URL
                </Row>
                <Row value="copy url" keywords={["clipboard", "domain"]} onSelect={() => void copy(pageSvcUrl, "URL")} icon={<Copy />}>
                  Copy URL
                </Row>
              </>
            )}
          </Group>
          {svc.envVarKeys.length > 0 && (
            <Group heading="Variables">
              {svc.envVarKeys.map((key) => (
                <Row key={`v-${key}`} value={`var ${key}`} keywords={["env"]} onSelect={() => go(envVarHref(svc, key), recent)} icon={<Variable />}>
                  <span className="font-mono text-[13px]">{key}</span>
                </Row>
              ))}
            </Group>
          )}
        </>
      );
    }
  }

  const crumbs = pages.map((p) => (p.kind === "project" ? p.project : (servicesByProject.get(p.project) ?? []).find((s) => s.name === p.name && s.group === p.group)?.display ?? p.name));
  const placeholder = !page
    ? "Search projects, services, variables, actions…"
    : page.kind === "project"
      ? `Search in ${page.project}…`
      : "Search actions…";

  return (
    <>
      <RollbackConfirm pending={pending?.kind === "rollback" ? pending.svc : null} onClose={() => setPending(null)} />
      <ConfirmDialog
        open={pending != null && pending.kind !== "rollback"}
        title={pending?.kind === "restart" ? `Restart ${pending.svc.display}?` : `Redeploy ${pending?.svc.display ?? ""}?`}
        body={
          pending?.kind === "restart"
            ? `Rolls the ${pending.svc.group} pods in place with the same image. Expect a brief blip if it runs a single replica.`
            : `Starts a new ${pending?.svc.group ?? "production"} build from ${
                pending?.svc.branch ? `branch ${pending.svc.branch}` : "the tracked branch"
              } and rolls the service when it's green.`
        }
        confirmLabel={pending?.kind === "restart" ? "Restart" : "Redeploy"}
        destructive={false}
        onCancel={() => setPending(null)}
        onConfirm={() => {
          if (pending) void runAction(pending);
        }}
      />
      <Dialog
        open={open}
        onOpenChange={(next, details) => {
          // Esc inside a submenu goes up one level instead of closing.
          if (!next && details.reason === "escape-key" && pages.length > 0) {
            popPage();
            return;
          }
          if (next) setOpen(true);
          else close();
        }}
      >
        <DialogContent
          showCloseButton={false}
          className="top-[12vh] max-w-[calc(100%-1.5rem)] translate-y-0 gap-0 overflow-hidden rounded-xl p-0 sm:max-w-[640px]"
        >
          <DialogTitle className="sr-only">Command palette</DialogTitle>
          <Command
            loop
            shouldFilter={false}
            value={selected}
            onValueChange={setSelected}
            label="Command palette"
            className="flex flex-col"
            onKeyDown={(e) => {
              if (e.key === "Backspace" && !search && pages.length > 0) {
                e.preventDefault();
                popPage();
                return;
              }
              // → drills only with the caret at the end, so it still moves
              // through the typed query.
              const input = inputRef.current;
              const caretAtEnd = !input || (input.selectionStart === input.value.length && input.selectionEnd === input.value.length);
              const wantsDrill = (e.key === "Tab" && !e.shiftKey) || (e.key === "ArrowRight" && caretAtEnd);
              const drill = wantsDrill ? drills.get(selected) : undefined;
              if (drill) {
                e.preventDefault();
                drill();
              }
            }}
          >
            <div className="flex items-center gap-2 border-b border-[var(--border-subtle)] px-3">
              {pages.length > 0 ? (
                <button
                  type="button"
                  onClick={popPage}
                  aria-label="Back"
                  className="flex h-6 w-6 shrink-0 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                >
                  <ArrowLeft className="h-4 w-4" />
                </button>
              ) : (
                <Search aria-hidden className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
              )}
              {crumbs.map((c, i) => (
                <span
                  key={`${c}-${i}`}
                  className="max-w-[9rem] shrink-0 truncate rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]"
                >
                  {c}
                </span>
              ))}
              <Command.Input
                ref={inputRef}
                value={search}
                onValueChange={setSearch}
                placeholder={placeholder}
                className="h-12 min-w-0 flex-1 bg-transparent text-[15px] text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)]"
              />
            </div>
            <Command.List ref={listRef} className="max-h-[min(460px,62vh)] overflow-y-auto overscroll-contain pb-1.5 [scroll-padding-block:8px]">
              {loadingIndex && (
                <Command.Loading>
                  <div className="px-4 pt-2 text-xs text-[var(--text-tertiary)]">Loading projects…</div>
                </Command.Loading>
              )}
              <Command.Empty className="px-4 py-10 text-center text-sm text-[var(--text-tertiary)]">
                {loadingIndex ? "Searching…" : <>No results for “{query}”.</>}
              </Command.Empty>
              <QueryContext.Provider value={query}>
                <DrillContext.Provider value={drills}>
                  <RankedGroups>{body}</RankedGroups>
                </DrillContext.Provider>
              </QueryContext.Provider>
            </Command.List>
            <div className="hidden items-center gap-4 border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-tertiary)] sm:flex">
              <span className="inline-flex items-center gap-1.5">
                <Kbd>↑</Kbd>
                <Kbd>↓</Kbd> to navigate
              </span>
              <span className="inline-flex items-center gap-1.5">
                <Kbd>↵</Kbd> to open
              </span>
              {pages[pages.length - 1]?.kind !== "service" && (
                <span className="inline-flex items-center gap-1.5">
                  <Kbd>→</Kbd> for actions
                </span>
              )}
              {pages.length > 0 && (
                <span className="inline-flex items-center gap-1.5">
                  <Kbd>⌫</Kbd> to go back
                </span>
              )}
              <span className="ml-auto inline-flex items-center gap-1.5">
                <Kbd>esc</Kbd> to {pages.length > 0 ? "go back" : "close"}
              </span>
            </div>
          </Command>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function CommandTrigger({ wide = false }: { wide?: boolean }) {
  // Server render and first paint say "Ctrl K"; swap to ⌘ after mount so
  // the static export doesn't hydrate-mismatch on Macs.
  const [mod, setMod] = useState("Ctrl");
  useEffect(() => setMod(isMac() ? "⌘" : "Ctrl"), []);
  return (
    <button
      type="button"
      onClick={() => {
        window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", metaKey: true }));
      }}
      className={cn(
        "inline-flex h-8 items-center gap-2 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2.5 text-xs text-[var(--text-tertiary)] transition-colors hover:border-[var(--border-default)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-secondary)]",
        wide && "w-full text-[13px]",
      )}
      aria-label="Open command palette"
      aria-keyshortcuts="Meta+K Control+K"
    >
      <Search className="h-3.5 w-3.5 shrink-0" />
      <span className="hidden truncate sm:inline">{wide ? "Jump to a project, service or action" : "Search"}</span>
      <span className={cn("hidden items-center gap-0.5 sm:inline-flex", wide ? "ml-auto" : "ml-2")}>
        <Kbd>{mod}</Kbd>
        <Kbd>K</Kbd>
      </span>
    </button>
  );
}

// RollbackConfirm resolves "the previous build" for the service's env when
// it opens, names both builds, and only then lets the user confirm.
function RollbackConfirm({ pending: svc, onClose }: { pending: PaletteService | null; onClose: () => void }) {
  const qc = useQueryClient();
  const builds = useBuilds(svc?.project ?? "", svc?.name ?? "");
  const envs = useEnvironments(svc?.project ?? "");
  const [busy, setBusy] = useState(false);
  // Skip builds that never became ready in this env: rolling back to a
  // release that crashed on start just repeats the outage.
  const env = svc
    ? envs.data?.find((e) => e.spec?.service === `${svc.project}-${svc.name}` && envGroupName(e) === svc.group)
    : undefined;
  const liveImages = liveImagesOf(env?.metadata?.annotations);
  const envRepo = (env?.spec as { image?: { repository?: string } } | undefined)?.image?.repository;
  const plan =
    svc && builds.data
      ? previousDeployTarget(builds.data, svc.group, (b) =>
          buildNeverReady(b, liveImages, envRepo, liveBuildId(builds.data, svc.group)),
        )
      : undefined;
  const run = async () => {
    if (!svc || !plan) return;
    setBusy(true);
    try {
      await rollbackToBuild(svc.project, svc.name, plan.target.id, svc.group);
      toast.success(`${svc.display} (${svc.group}) now runs ${buildRefLabel(plan.target, 7) || plan.target.id}`);
      void qc.invalidateQueries({ queryKey: buildsQueryKey(svc.project, svc.name) });
      onClose();
    } catch (err) {
      toastError(err, `Couldn't roll back ${svc.display}`);
    } finally {
      setBusy(false);
    }
  };
  const ref = (b: { commitSha?: string; ref?: string; id: string }) => buildRefLabel(b, 7) || b.id;
  return (
    <ConfirmDialog
      open={!!svc}
      title={svc ? `Roll back ${svc.display}?` : ""}
      destructive={false}
      confirmLabel={plan ? `Roll back to ${ref(plan.target)}` : "Roll back"}
      pending={busy || builds.isPending || !plan}
      body={
        !svc ? null : builds.isPending ? (
          "Finding the previous build…"
        ) : builds.isError ? (
          "Couldn't load this service's builds."
        ) : !plan ? (
          `There's no earlier build of ${svc.display} in ${svc.group} with an image to go back to.`
        ) : (
          <span className="block space-y-2">
            <span className="block">
              <strong className="text-[var(--text-primary)]">{svc.group}</strong> switches from{" "}
              <span className="font-mono">{ref(plan.live)}</span> to{" "}
              <span className="font-mono">{ref(plan.target)}</span> ({buildTitle(plan.target)}) right away, with no rebuild.
            </span>
            {plan.sameCommit && (
              <span className="block">Same commit, older image: the code doesn&apos;t change.</span>
            )}
            <span className="block">The next push to {plan.target.branch || "the branch"} deploys over it.</span>
          </span>
        )
      }
      onCancel={onClose}
      onConfirm={() => void run()}
    />
  );
}
