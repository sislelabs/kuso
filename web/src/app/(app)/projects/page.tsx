"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useProjects, useStopProject, useStartProject } from "@/features/projects";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useInstallations } from "@/features/github";
import { useCan, Perms } from "@/features/auth";
import { useProjectPrefs, useSetProjectPref } from "@/features/userprefs";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { LayoutGrid, Plus, ArrowUpRight, GitBranch, Globe, Box, Database, Cpu, MemoryStick, Settings, Star, FolderPlus, Folder, ChevronDown, Power, Pause } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { useQueries } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import type { KusoEnvironment, KusoService, KusoAddon } from "@/types/projects";

// ProjectsPage is the landing dashboard listing every project. Each
// row is a thin card showing the name, repo, base domain, and a small
// "open" affordance. We deliberately don't pre-fetch deploy status —
// the canvas has the live picture, this page is just navigation.
export default function ProjectsPage() {
  const router = useRouter();
  const { data, isPending, isError, error, refetch } = useProjects();
  // Project creation is an instance-admin action in role-system v2
  // (editors are granted access to EXISTING projects; only admins make
  // new ones). Non-admins still see + open projects they're granted —
  // the server filters /api/projects to their grants.
  const canCreate = useCan(Perms.SettingsAdmin);
  const installations = useInstallations();

  // First-run redirect: a freshly-logged-in user landing on /projects
  // with zero projects AND zero GitHub installations gets bounced to
  // the guided onboarding. Only fires for users who can create
  // projects.
  //
  // Loop trap: the user can leave /welcome via "skip to dashboard,"
  // which routes back to /projects. Without a memo we'd bounce them
  // straight back to /welcome — the back button becomes a trap and
  // the only escape is closing the tab. Remember in sessionStorage
  // that we've redirected once this tab-session and don't fire again
  // even if the no-projects/no-installations precondition still
  // holds. Cleared on next login (sessionStorage scope).
  useEffect(() => {
    if (isPending || installations.isPending) return;
    if (!canCreate) return;
    if ((data?.length ?? 0) > 0) return;
    if ((installations.data?.length ?? 0) > 0) return;
    try {
      if (sessionStorage.getItem("kuso.welcome.redirected") === "1") return;
      sessionStorage.setItem("kuso.welcome.redirected", "1");
    } catch {
      // Storage may throw in private-browsing modes; fall through
      // and redirect anyway. Worst case is one extra loop, which
      // is the same as today's behaviour.
    }
    router.replace("/welcome");
  }, [isPending, installations.isPending, canCreate, data, installations.data, router]);

  return (
    <div className="mx-auto max-w-6xl p-6 lg:p-8">
      <header className="mb-6 flex items-end justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Projects</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Each project is one product. Connect a repo, kuso builds it on every push.
          </p>
        </div>
        {canCreate && (
          <Link
            href="/projects/new"
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] shadow-[var(--shadow-sm)] transition-colors hover:bg-[var(--btn-primary-bg-hover)] hover:scale-[1.02]"
          >
            <Plus className="h-3.5 w-3.5" />
            New project
          </Link>
        )}
      </header>

      {isPending && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-28 w-full rounded-md" />
          ))}
        </div>
      )}

      {isError && (
        <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4">
          <p className="text-sm text-red-400">
            Failed to load projects: {error?.message ?? "unknown error"}
          </p>
          <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
            Retry
          </Button>
        </div>
      )}

      {!isPending && !isError && data && data.length === 0 && (
        <EmptyState
          icon={<LayoutGrid className="h-5 w-5" />}
          title="No projects yet"
          description="Connect a GitHub repo and kuso will build, deploy, and give you a live URL. Already running Coolify? Import from there in one step."
          action={
            <div className="flex flex-wrap items-center gap-2">
              <Link
                href="/projects/new"
                className="inline-flex h-8 items-center gap-1.5 rounded-md bg-[var(--accent)] px-3 text-xs font-medium text-[var(--accent-foreground)] hover:bg-[var(--accent)]/90"
              >
                <Plus className="h-3.5 w-3.5" />
                Create your first project
              </Link>
              <Link
                href="/settings/import"
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-transparent px-3 text-xs font-medium text-[var(--text-primary)] hover:bg-[var(--bg-secondary)]"
              >
                Import from Coolify
              </Link>
            </div>
          }
        />
      )}

      {!isPending && !isError && data && data.length > 0 && (
        // Stable alphabetical order by name. The server returns
        // KusoProject CRs in whatever order the kube apiserver
        // returns them (last-modified-ish, ETCD-internal), which
        // shuffles the grid every time you visit. Alpha by name is
        // predictable + deterministic across refreshes.
        <ProjectsGrid
          projects={[...data].sort((a, b) =>
            a.metadata.name.localeCompare(b.metadata.name),
          )}
        />
      )}
    </div>
  );
}

function Row({
  icon: Icon,
  value,
  href,
}: {
  icon: React.ComponentType<{ className?: string }>;
  value: string;
  // When set the row becomes an external <a> opening href in a new
  // tab. Used by the domain row so users can click the public URL
  // straight off the project card. We stopPropagation so clicking
  // the link doesn't also trigger the parent card's nav handler.
  href?: string;
}) {
  const body = (
    <>
      <Icon className="h-3 w-3 shrink-0" />
      <span className="truncate font-mono text-[var(--text-secondary)]">{value}</span>
    </>
  );
  if (href) {
    return (
      <a
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        onClick={(e) => e.stopPropagation()}
        // Opt back into pointer events — the parent card has
        // `pointer-events-none` on its content so card clicks land on
        // the overlay <Link>. The domain row is the one nested
        // interactive child; this puts it back into the click flow
        // (and above the overlay via z-30) so the user can click
        // straight to the live site.
        // `flex w-fit` (not inline-flex) so each row is its own block and
        // the <dl>'s space-y stacks them vertically — inline-flex made
        // two linked rows (repo + domain) sit side-by-side on one line.
        className="pointer-events-auto relative z-30 flex w-fit items-center gap-1.5 text-[var(--text-tertiary)] transition-colors hover:text-[var(--accent)]"
      >
        {body}
      </a>
    );
  }
  return (
    <div className="flex items-center gap-1.5 text-[var(--text-tertiary)]">{body}</div>
  );
}

// ProjectMetricsResp matches the server's projectMetricsResponse JSON
// shape (kubernetes_env_metrics.go). Kept inline rather than a feature
// module — this is the only consumer.
interface ProjectMetricsResp {
  project: string;
  cpuMillicores: number;
  memBytes: number;
  pods: number;
  envs: number;
}

// formatMillicores collapses millicores to a tight string: "142m" up to
// 999m, then "1.4" cores. Drops trailing zeros so "1.0" reads as "1".
function formatMillicores(m: number): string {
  if (m < 1000) return `${m}m`;
  const cores = m / 1000;
  return cores >= 10 ? `${cores.toFixed(0)}` : `${cores.toFixed(1).replace(/\.0$/, "")}`;
}

// formatBytes uses MiB / GiB so the units match metrics-server (binary,
// not decimal). Project cards skip the suffix verbosity — "384 MiB" is
// enough; we don't show "384.32 MiB".
function formatBytes(b: number): string {
  if (b <= 0) return "0 MiB";
  const MiB = 1024 * 1024;
  const GiB = 1024 * MiB;
  if (b >= GiB) {
    const v = b / GiB;
    return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} GiB`;
  }
  return `${Math.round(b / MiB)} MiB`;
}

// repoWebURL normalises a stored git clone URL to a browser-openable
// https:// link, or null when it can't form a confident one (so the
// repo row stays plain text instead of linking somewhere wrong).
//
// Handles the three shapes kuso stores:
//   https://github.com/org/repo(.git)   → as-is, .git stripped
//   git@github.com:org/repo(.git)        → https://github.com/org/repo
//   github.com/org/repo(.git)            → https://github.com/org/repo
// distinctServiceRepo returns the single repo URL shared by all of a
// project's services that declare one, or undefined when there are none
// or they disagree (a genuine multi-repo project — the card then shows
// no repo rather than picking arbitrarily). Used as the card's repo
// fallback when the project has no defaultRepo.
function distinctServiceRepo(services: ReadonlyArray<KusoService>): string | undefined {
  const urls = new Set<string>();
  for (const s of services) {
    const u = s.spec.repo?.url;
    if (u) urls.add(u);
  }
  return urls.size === 1 ? [...urls][0] : undefined;
}

// distinctServiceDomain returns the single primary custom domain shared
// across a project's services (or the lone service's domain), or
// undefined when services disagree / none have one. Used as the card's
// display-domain fallback when the project has no baseDomain.
function distinctServiceDomain(services: ReadonlyArray<KusoService>): string | undefined {
  const hosts = new Set<string>();
  for (const s of services) {
    const h = s.spec.domains?.[0]?.host;
    if (h) hosts.add(h);
  }
  return hosts.size === 1 ? [...hosts][0] : undefined;
}

// isFrontendService: a web-facing service worth surfacing as the project's
// public face. Heuristic from the fields the client models: a declared HTTP
// port means it serves traffic (workers have no port). Used to pick which
// service's default host represents the project on the card.
function isFrontendService(s: KusoService): boolean {
  return (s.spec.port ?? 0) > 0;
}

// defaultProjectHost is the card's display-domain fallback when the project
// has no baseDomain AND no custom service domain: use the kuso-default host
// (e.g. web.<project>.kuso.sislelabs.com) of the DETECTED FRONTEND service's
// production environment — or, if no frontend is detected, the FIRST service.
// The host lives on the production KusoEnvironment (.spec.host), not the
// service. Returns undefined when there are no services / no env host yet.
function defaultProjectHost(
  services: ReadonlyArray<KusoService>,
  environments: ReadonlyArray<KusoEnvironment>,
): string | undefined {
  if (services.length === 0) return undefined;
  // Match a service to its production env via the kuso.sislelabs.com/service
  // label on both — robust to hyphenated project/service names (no CR-name
  // parsing). Falls back to a single-service project's lone production host.
  const svcLabel = (s: KusoService) => s.metadata?.labels?.["kuso.sislelabs.com/service"];
  const prodHostForService = (s: KusoService): string | undefined => {
    const want = svcLabel(s);
    let lone: string | undefined;
    let count = 0;
    for (const e of environments) {
      if (e.spec?.kind !== "production" || !e.spec?.host) continue;
      count++;
      lone = e.spec.host;
      if (want && e.metadata?.labels?.["kuso.sislelabs.com/service"] === want) {
        return e.spec.host;
      }
    }
    // No label match but exactly one production env → it's this service's.
    return count === 1 ? lone : undefined;
  };
  // Detected frontend first (in service order = creation order from the API),
  // then fall back to the first service of any kind.
  const pick = services.find(isFrontendService) ?? services[0];
  return prodHostForService(pick);
}

function repoWebURL(raw?: string): string | null {
  if (!raw) return null;
  let s = raw.trim();
  if (!s) return null;
  // scp-style SSH: git@host:org/repo → host/org/repo
  const scp = s.match(/^[\w.-]+@([\w.-]+):(.+)$/);
  if (scp) {
    s = `${scp[1]}/${scp[2]}`;
  } else {
    // strip any scheme (https://, http://, ssh://, git://)
    s = s.replace(/^[a-z]+:\/\//i, "");
  }
  // drop a userinfo@ prefix if a scheme carried one (ssh://git@host/…)
  s = s.replace(/^[^/@]+@/, "");
  s = s.replace(/\.git$/, "").replace(/\/+$/, "");
  // Require a host/path shape (at least "host/owner/repo"-ish). Bail on
  // anything without a dot-bearing host + a path segment.
  if (!/^[\w.-]+\.[\w.-]+\/.+/.test(s)) return null;
  return `https://${s}`;
}

interface DescribeResp {
  project: { metadata: { name: string }; spec?: { baseDomain?: string } };
  services: KusoService[];
  environments: KusoEnvironment[];
  addons?: KusoAddon[];
}

// ProjectsGrid fans out a /api/projects/{name} fetch per card so each
// shows live counts (services up, total services, addons). Listing
// already returns the project specs, but Describe is the only call
// that bundles services + envs in one round-trip — cheaper than
// per-card fetches that paginate them separately.
//
// Use useQueries (not N useQuery hooks) so the rules-of-hooks
// invariant holds when projects come and go.
function ProjectsGrid({
  projects,
}: {
  projects: ReadonlyArray<{ metadata: { name: string; uid?: string; creationTimestamp?: string }; spec: { defaultRepo?: { url?: string }; baseDomain?: string; description?: string } }>;
}) {
  // Gates the per-card settings shortcut — only instance admins manage
  // project settings.
  const canManage = useCan(Perms.SettingsAdmin);
  // Gate the card's stop-project button on settings:admin (same as the
  // settings gear). NOTE: services:write is a PROJECT-scoped permission
  // (PermsForProjectRole), never present in the session/instance perm set
  // useCan checks — so useCan(ServicesWrite) is false for EVERYONE, admins
  // included, which made this button dead. This list page renders N cards
  // in a .map(), so per-project role hooks (useCanOnProject) can't be
  // called here; settings:admin is the honest instance-level gate. The
  // server still enforces ProjectRoleEditor on the endpoint regardless.
  const canStopProjects = canManage;
  // Whole-project stop/start. The power button in each card's icon row
  // starts a project directly (one click, safe) but routes a stop
  // through a typed confirm — it 503s every visitor until start.
  const stopProject = useStopProject();
  const startProject = useStartProject();
  // Which project (if any) is awaiting stop confirmation, and how many
  // services that stop would hit (for the dialog copy). Lives at the
  // grid level so the single ConfirmDialog renders once, not per card.
  const [confirmStop, setConfirmStop] = useState<{
    name: string;
    count: number;
  } | null>(null);
  // Per-user grid prefs: starred projects pin to the top, folders group
  // the rest. byProject is a Map for O(1) per-card lookup.
  const { byProject: prefs } = useProjectPrefs();
  const setPref = useSetProjectPref();
  // Known folder labels across the user's prefs — offered in each card's
  // "move to folder" menu so filing into an existing folder is one click.
  const knownFolders = Array.from(
    new Set(
      Array.from(prefs.values())
        .map((p) => p.folder)
        .filter((f): f is string => !!f)
    )
  ).sort((a, b) => a.localeCompare(b));
  const queries = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["projects", p.metadata.name, "describe-summary"],
      queryFn: () => api<DescribeResp>(`/api/projects/${encodeURIComponent(p.metadata.name)}`),
      staleTime: 15_000,
    })),
  });
  // Per-project CPU/RAM rollup, polled every 30s. metrics-server
  // emits new samples every 15s so 30s gives near-fresh data without
  // hammering the API. Failures fall through to empty values — the
  // server already returns 200 + zeros on metrics-server outage so we
  // shouldn't see errors in practice, but guard anyway.
  const metricQueries = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["projects", p.metadata.name, "metrics-rollup"],
      queryFn: () =>
        api<ProjectMetricsResp>(`/api/projects/${encodeURIComponent(p.metadata.name)}/metrics`),
      refetchInterval: 30_000,
      staleTime: 25_000,
    })),
  });
  // Stable fingerprints of the two query arrays: change only when a query
  // resolves new data (dataUpdatedAt advances), not on every render. Used
  // as the cards-memo deps so a metrics poll that returns identical data
  // doesn't rebuild all N cards.
  const queriesFingerprint = queries.map((q) => q.dataUpdatedAt).join(",");
  const metricsFingerprint = metricQueries.map((q) => q.dataUpdatedAt).join(",");
  // Build one rendered <li> per project, tagged with its star/folder
  // state so we can group them into sections below without recomputing
  // the per-card data (which is indexed off the queries arrays by
  // position).
  //
  // Memoised: this map builds ~300 lines of JSX per project and was
  // re-running on EVERY render — including every 30s metrics-poll tick
  // and every star/folder toggle — rebuilding all N cards even when only
  // one project's data changed. At 50 projects that's a lot of wasted
  // reconciliation. Keyed on the query/pref inputs so it only rebuilds
  // when the underlying data actually moves.
  const cards = useMemo(() => projects.map((p, i) => {
        const name = p.metadata.name;
        const pref = prefs.get(name);
        const starred = pref?.starred ?? false;
        const folder = pref?.folder ?? "";
        const created = p.metadata.creationTimestamp
          ? relativeTime(p.metadata.creationTimestamp)
          : null;
        const summary = queries[i]?.data;
        const services = summary?.services ?? [];
        const environments = summary?.environments ?? [];
        // Display domain, in priority order:
        //   1. project baseDomain (explicit),
        //   2. a single custom domain shared across services,
        //   3. the kuso-default host of the detected frontend service's
        //      production env (or the first service's) — so service-first
        //      projects with no custom domain still show where the app
        //      lives (e.g. web.<project>.kuso.sislelabs.com) instead of a
        //      blank domain row.
        const domain =
          p.spec.baseDomain ??
          distinctServiceDomain(services) ??
          defaultProjectHost(services, environments);
        // Effective repo for the card. Prefer the project's defaultRepo;
        // when it has none (single-service / service-first projects where
        // the repo lives on the service), fall back to the services' own
        // repos — but only when unambiguous (every service that has a
        // repo points at the SAME one). A multi-repo project shows no
        // repo row rather than arbitrarily picking one.
        const effectiveRepoRaw =
          p.spec.defaultRepo?.url ?? distinctServiceRepo(services);
        const repo = effectiveRepoRaw?.replace(/^https?:\/\/(www\.)?/, "");
        // Clickable web URL for the repo row. Normalises the stored
        // clone URL (https://…, git@host:…, or bare host/path, with or
        // without a trailing .git) to an https:// browser link. Returns
        // null when we can't form a confident URL so the row stays
        // plain text rather than linking somewhere wrong.
        const repoURL = repoWebURL(effectiveRepoRaw);
        const envs = summary?.environments ?? [];
        const addons = summary?.addons ?? [];
        // "Live" = a production env with at least one pod actually
        // serving traffic. Sources of truth (in order):
        //   1. status.replicas.ready > 0  — the canvas's own check.
        //   2. status.ready === true      — helm release Deployed AND
        //                                    the operator marked the
        //                                    env Ready.
        //
        // We deliberately do NOT count `desired > 0` as live — every
        // scaled service has desired > 0 the moment it's created, but
        // before its first build lands there are zero ready pods.
        // The canvas shows `0/5 ready` in that state; the project
        // card here used to read it as "5/5 LIVE", which lied.
        const liveServices = services.filter((s) => {
          const fqn = s.metadata.name;
          const prod = envs.find(
            (e) => e.spec.service === fqn && e.spec.kind === "production"
          );
          if (!prod) return false;
          const st = prod.status as
            | {
                ready?: boolean;
                phase?: string;
                replicas?: { ready?: number; desired?: number; max?: number };
              }
            | undefined;
          if ((st?.replicas?.ready ?? 0) > 0) return true;
          if (st?.ready === true) return true;
          return false;
        }).length;
        // Count services the user has hard-stopped (spec.stopped=true).
        // A stopped service reports 0 ready pods, so without this the
        // health line would render it as "down/degraded" (red/amber)
        // when it's actually an intentional stop — shown slate instead.
        const stoppedServices = services.filter(
          (s) => s.spec.stopped === true
        ).length;
        // Whole-project state: every service stopped, or a mix.
        const allStopped =
          services.length > 0 && stoppedServices === services.length;
        const anyRunning = services.length > 0 && stoppedServices < services.length;
        // Public URL of the live deployment, surfaced TWICE on the card:
        //   1. The domain `Row` becomes an <a> opening it in a new tab.
        //   2. Used by the body of the card if we ever want a "visit
        //      site" affordance separate from "open in kuso".
        // We prefer the first live service with a configured domain so
        // multi-service projects (frontend + api + worker) link to the
        // user-facing one. Falls back to any service-with-domain so an
        // offline project still has an obvious link.
        const publicURL = (() => {
          const pick = (s: (typeof services)[number] | undefined) => {
            const host = s?.spec.domains?.[0]?.host;
            if (!host) return null;
            const scheme = s?.spec.domains?.[0]?.tls === false ? "http" : "https";
            return `${scheme}://${host}`;
          };
          const live = services.find((s) => {
            const prod = envs.find(
              (e) => e.spec.service === s.metadata.name && e.spec.kind === "production"
            );
            const st = prod?.status as
              | { ready?: boolean; replicas?: { ready?: number } }
              | undefined;
            const isLive =
              (st?.replicas?.ready ?? 0) > 0 || st?.ready === true;
            return isLive && (s.spec.domains?.[0]?.host ?? "") !== "";
          });
          if (live) return pick(live);
          const anyWithDomain = services.find(
            (s) => (s.spec.domains?.[0]?.host ?? "") !== ""
          );
          return pick(anyWithDomain);
        })();
        const metrics = metricQueries[i]?.data;
        const node = (
          <li key={p.metadata.uid ?? name} className="h-full">
            {/* Card layout: the whole card is one big <Link> into
                kuso, with nested clickable bits (the external domain
                row) escaped via pointer-events. We previously used an
                absolutely-positioned overlay <Link> at z-0 with the
                content at z-10, which made the content block clicks
                from reaching the link — the user could only navigate
                by clicking the small gaps between rows. The fix:
                pointer-events-none on the link overlay's content, and
                pointer-events-auto on the one nested <a> that needs
                its own click. The overlay <Link> stays at z-10
                covering the whole card.

                h-full + flex-col makes every card fill its grid row so
                cards in the same row are equal height regardless of how
                many body rows (description, addons, metrics) they have. */}
            <div className="group relative flex h-full cursor-pointer flex-col rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40">
              <Link
                href={`/projects/${name}`}
                aria-label={`Open project ${name}`}
                className="absolute inset-0 z-10 rounded-md"
              />
              {/* Content sits BELOW the overlay <Link> but is visible
                  because the overlay is transparent. pointer-events-
                  none on the content tree means clicks pass through
                  to the link — the one interactive child (the domain
                  row's external <a>) opts back into pointer events on
                  itself. */}
              <div className="pointer-events-none relative z-20">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <h2 className="truncate text-sm font-semibold tracking-tight text-[var(--text-primary)] transition-colors group-hover:text-[var(--accent)]">
                      {name}
                    </h2>
                    {p.spec.description && (
                      <p className="mt-1 line-clamp-2 text-[12px] text-[var(--text-secondary)]">
                        {p.spec.description}
                      </p>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    {/* Star toggle — pins this project to the top
                        "Starred" section of the grid. pointer-events-auto
                        + z-30 so it acts on itself, not the card link.
                        Starred state shows a filled amber star. */}
                    <button
                      type="button"
                      aria-label={starred ? `Unstar ${name}` : `Star ${name}`}
                      aria-pressed={starred}
                      title={starred ? "Unstar" : "Star"}
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setPref.mutate({ project: name, starred: !starred, folder });
                      }}
                      className="pointer-events-auto relative z-30 inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                    >
                      <Star
                        className={
                          "h-3.5 w-3.5 " +
                          (starred ? "fill-amber-400 text-amber-400" : "")
                        }
                      />
                    </button>
                    {/* Move-to-folder menu (Popover, NOT dropdown-menu —
                        base-ui Menu has a hydration edge case in our
                        static export; see CLAUDE.md). Lists existing
                        folders + "New folder…" + "Remove from folder". */}
                    <FolderMenu
                      project={name}
                      currentFolder={folder}
                      knownFolders={knownFolders}
                      onPick={(nextFolder) =>
                        setPref.mutate({ project: name, starred, folder: nextFolder })
                      }
                    />
                    {/* Settings shortcut. Sits above the overlay <Link>
                        (pointer-events-auto + higher z) so it navigates
                        to the project's settings instead of the card's
                        default "open project" target. */}
                    {canManage && (
                      <Link
                        href={`/projects/${name}/settings`}
                        aria-label={`Settings for ${name}`}
                        title="Project settings"
                        onClick={(e) => e.stopPropagation()}
                        className="pointer-events-auto relative z-30 inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                      >
                        <Settings className="h-3.5 w-3.5" />
                      </Link>
                    )}
                    {/* Whole-project power toggle. Only rendered once the
                        project has at least one service. anyRunning →
                        "Stop project" (routes through a typed confirm,
                        since it 503s visitors); allStopped → "Start
                        project" (one click). pointer-events-auto + z-30
                        so it acts on itself, not the card's overlay Link
                        — same escape pattern as the Star button. */}
                    {canStopProjects && services.length > 0 && (
                      <button
                        type="button"
                        aria-label={
                          allStopped
                            ? `Start project ${name}`
                            : `Stop project ${name}`
                        }
                        title={allStopped ? "Start project" : "Stop project"}
                        disabled={
                          stopProject.isPending || startProject.isPending
                        }
                        onClick={(e) => {
                          e.preventDefault();
                          e.stopPropagation();
                          if (allStopped) {
                            startProject.mutate(name);
                          } else if (anyRunning) {
                            setConfirmStop({
                              name,
                              count: services.length,
                            });
                          }
                        }}
                        className={
                          "pointer-events-auto relative z-30 inline-flex h-6 w-6 items-center justify-center rounded-md hover:bg-[var(--bg-tertiary)] disabled:opacity-40 " +
                          (allStopped
                            ? "text-slate-400 hover:text-emerald-400"
                            : "text-[var(--text-tertiary)] hover:text-red-400")
                        }
                      >
                        {allStopped ? (
                          <Power className="h-3.5 w-3.5" />
                        ) : (
                          <Pause className="h-3.5 w-3.5" />
                        )}
                      </button>
                    )}
                    {/* The arrow signals the whole card is a link into
                        kuso. The overlay <Link> handles the actual
                        nav; this is purely affordance. */}
                    <ArrowUpRight
                      aria-hidden
                      className="h-3.5 w-3.5 text-[var(--text-tertiary)] transition-colors group-hover:text-[var(--accent)]"
                    />
                  </div>
                </div>
                <dl className="mt-3 space-y-1 text-[11px]">
                  {repo && <Row icon={GitBranch} value={repo} href={repoURL ?? undefined} />}
                  {domain && (
                    <Row
                      icon={Globe}
                      value={domain}
                      // The link must match the text. When the card shows
                      // the project's BASE domain, link there directly —
                      // not to publicURL (the first live service's host,
                      // e.g. api.<base>), which sent "distill.sislelabs.com"
                      // to "api.distill.sislelabs.com". publicURL is only
                      // the right target when the displayed domain came
                      // from a service host (baseless projects), where it
                      // and the service URL are the same host.
                      href={
                        p.spec.baseDomain
                          ? `https://${p.spec.baseDomain}`
                          : publicURL ?? (domain ? `https://${domain}` : undefined)
                      }
                    />
                  )}
                </dl>
              {summary && (
                <div className="mt-3 flex items-center gap-3 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  {(() => {
                    // Health is conveyed by colour AND an a11y label
                    // so screen readers and colour-blind users get
                    // the same signal as sighted-trichromat users.
                    // Without the label, "2/3 live" with a green/
                    // amber/red colour modifier was just "2/3 live"
                    // to a screen reader — useful but lossy.
                    let cls = "";
                    let healthLabel = `${liveServices} of ${services.length} services live`;
                    if (services.length === 0) {
                      healthLabel = "no services yet";
                    } else if (liveServices === 0) {
                      cls = "text-red-400";
                      healthLabel = `down — 0 of ${services.length} services live`;
                    } else if (liveServices < services.length) {
                      cls = "text-amber-400";
                      healthLabel = `degraded — ${liveServices} of ${services.length} services live`;
                    } else {
                      cls = "text-emerald-400";
                      healthLabel = `healthy — all ${services.length} services live`;
                    }
                    // Stopped services report 0 ready pods, so the
                    // live/down logic above would mislabel an
                    // intentional stop as an outage. When ALL services
                    // are stopped, show a muted "stopped" pill (slate,
                    // matching the canvas node's stopped styling); when
                    // SOME are stopped, append "· K stopped" so a
                    // partial stop reads distinct from a real degrade.
                    if (allStopped) {
                      return (
                        <span
                          className="inline-flex items-center gap-1"
                          aria-label={`stopped — all ${services.length} services stopped`}
                        >
                          <Pause className="h-3 w-3" aria-hidden />
                          <span className="text-slate-400">
                            0/{services.length}
                          </span>
                          <span aria-hidden className="text-slate-400">
                            stopped
                          </span>
                        </span>
                      );
                    }
                    return (
                      <span
                        className="inline-flex items-center gap-1"
                        aria-label={
                          stoppedServices > 0
                            ? `${healthLabel}, ${stoppedServices} stopped`
                            : healthLabel
                        }
                      >
                        <Box className="h-3 w-3" aria-hidden />
                        <span className={cls}>
                          {liveServices}/{services.length}
                        </span>
                        <span aria-hidden>live</span>
                        {stoppedServices > 0 && (
                          <span aria-hidden className="text-slate-400">
                            · {stoppedServices} stopped
                          </span>
                        )}
                      </span>
                    );
                  })()}
                  {addons.length > 0 && (
                    <span className="inline-flex items-center gap-1">
                      <Database className="h-3 w-3" />
                      <span>{addons.length}</span>
                      <span>addon{addons.length === 1 ? "" : "s"}</span>
                    </span>
                  )}
                  {/* Resource line — only shown when we actually have
                      pod metrics. Skipped for offline projects so the
                      card doesn't render a misleading "0m · 0 MiB"
                      that could be confused with "metrics-server down".
                      30s poll handled by the parent useQueries. */}
                  {metrics && metrics.pods > 0 && (
                    <>
                      <span
                        className="inline-flex items-center gap-1"
                        aria-label={`${formatMillicores(metrics.cpuMillicores)} CPU across ${metrics.pods} pod${metrics.pods === 1 ? "" : "s"}`}
                      >
                        <Cpu className="h-3 w-3" aria-hidden />
                        <span>{formatMillicores(metrics.cpuMillicores)}</span>
                      </span>
                      <span
                        className="inline-flex items-center gap-1"
                        aria-label={`${formatBytes(metrics.memBytes)} memory`}
                      >
                        <MemoryStick className="h-3 w-3" aria-hidden />
                        <span>{formatBytes(metrics.memBytes)}</span>
                      </span>
                    </>
                  )}
                </div>
              )}
              {created && (
                <p className="mt-3 border-t border-[var(--border-subtle)] pt-2 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  created {created}
                </p>
              )}
              </div>
            </div>
          </li>
        );
        return { name, starred, folder, node };
      }),
    // useQueries returns a fresh array wrapper every render even when no
    // data changed, so depending on `queries`/`metricQueries` directly
    // would defeat the memo. Fingerprint on the per-query dataUpdatedAt
    // (changes only when a query actually resolves new data) + prefs, so
    // a 30s metrics tick that returns identical data is a no-op rebuild.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      projects,
      // prefs is the byProject Map — now memoised in useProjectPrefs on the
      // query's dataUpdatedAt, so its ref is stable across renders and only
      // changes when the pref data actually moves. Listing it here is safe.
      prefs,
      queriesFingerprint,
      metricsFingerprint,
      canManage,
      canStopProjects,
      // NOTE: setPref/stopProject/startProject are intentionally NOT in the
      // deps. They're useMutation() results — a fresh object reference every
      // render — so listing them would invalidate this memo on every render
      // and rebuild all N cards, defeating the whole point of the memo (see
      // the comment above). Their `.mutate` fns are stable, so the closure
      // captures a working reference regardless.
    ]);

  // Group the cards into sections: Starred (pinned to top), then each
  // folder alphabetically, then Unfiled. Within a section cards keep the
  // incoming alphabetical order. A starred project shows ONLY in Starred
  // (its folder is still recorded, just not double-listed) so the top
  // section is the user's true shortlist.
  const starredCards = cards.filter((c) => c.starred);
  const unstarred = cards.filter((c) => !c.starred);
  const folderNames = Array.from(
    new Set(unstarred.map((c) => c.folder).filter((f) => f !== ""))
  ).sort((a, b) => a.localeCompare(b));
  const unfiledCards = unstarred.filter((c) => c.folder === "");

  const grid = (items: typeof cards) => (
    <ul className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {items.map((c) => c.node)}
    </ul>
  );

  // One shared stop-confirm modal for the whole grid — the per-card
  // power buttons set `confirmStop`; this renders it. typeToConfirm =
  // the project name because a project-wide stop 503s every visitor.
  const stopDialog = (
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
            This stops ALL {confirmStop.count} service
            {confirmStop.count === 1 ? "" : "s"} in{" "}
            <span className="font-mono text-[var(--text-primary)]">
              {confirmStop.name}
            </span>
            . Visitors get a 503 until you start it again.
          </p>
        ) : null
      }
      onConfirm={() => {
        if (confirmStop) stopProject.mutate(confirmStop.name);
        setConfirmStop(null);
      }}
      onCancel={() => setConfirmStop(null)}
    />
  );

  // No prefs at all → the original flat grid, no section chrome.
  const hasSections = starredCards.length > 0 || folderNames.length > 0;
  if (!hasSections)
    return (
      <>
        {grid(cards)}
        {stopDialog}
      </>
    );

  return (
    <div className="space-y-6">
      {stopDialog}
      {starredCards.length > 0 && (
        <Section
          icon={<Star className="h-3.5 w-3.5 fill-amber-400 text-amber-400" />}
          label="Starred"
          count={starredCards.length}
        >
          {grid(starredCards)}
        </Section>
      )}
      {folderNames.map((fname) => {
        const items = unstarred.filter((c) => c.folder === fname);
        return (
          <Section
            key={fname}
            icon={<Folder className="h-3.5 w-3.5" />}
            label={fname}
            count={items.length}
          >
            {grid(items)}
          </Section>
        );
      })}
      {unfiledCards.length > 0 && (
        <Section label="Unfiled" count={unfiledCards.length} muted>
          {grid(unfiledCards)}
        </Section>
      )}
    </div>
  );
}

// Section is a collapsible group header + its card grid. Used to render
// the Starred / per-folder / Unfiled groupings on the projects page.
// Collapse state is local (per-session) — folder identity already
// persists server-side; whether a section is open is ephemeral UI.
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
        className={
          "mb-2 flex w-full items-center gap-1.5 text-[11px] font-medium uppercase tracking-widest " +
          (muted ? "text-[var(--text-tertiary)]" : "text-[var(--text-secondary)]")
        }
      >
        <ChevronDown
          className={"h-3 w-3 transition-transform " + (open ? "" : "-rotate-90")}
          aria-hidden
        />
        {icon}
        <span>{label}</span>
        <span className="text-[var(--text-tertiary)]">{count}</span>
      </button>
      {open && children}
    </section>
  );
}

// FolderMenu is the per-card "move to folder" Popover. Lists existing
// folders for one-click filing, plus inline "new folder" entry and a
// "remove from folder" affordance when the card is already filed.
function FolderMenu({
  project,
  currentFolder,
  knownFolders,
  onPick,
}: {
  project: string;
  currentFolder: string;
  knownFolders: string[];
  onPick: (folder: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const pick = (folder: string) => {
    onPick(folder);
    setOpen(false);
    setNewName("");
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label={`Organize ${project}`}
        title="Move to folder"
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
        }}
        className={
          "pointer-events-auto relative z-30 inline-flex h-6 w-6 items-center justify-center rounded-md hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] " +
          (currentFolder ? "text-[var(--text-secondary)]" : "text-[var(--text-tertiary)]")
        }
      >
        {currentFolder ? <Folder className="h-3.5 w-3.5" /> : <FolderPlus className="h-3.5 w-3.5" />}
      </PopoverTrigger>
      <PopoverContent
        align="end"
        className="w-52 p-1"
        onClick={(e) => e.stopPropagation()}
      >
        <p className="px-2 py-1.5 text-[10px] font-medium uppercase tracking-widest text-[var(--text-tertiary)]">
          Move to folder
        </p>
        {knownFolders.map((f) => (
          <button
            key={f}
            type="button"
            onClick={() => pick(f)}
            className={
              "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[13px] hover:bg-[var(--bg-tertiary)] " +
              (f === currentFolder ? "text-[var(--accent)]" : "text-[var(--text-primary)]")
            }
          >
            <Folder className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{f}</span>
            {f === currentFolder && <span className="ml-auto text-[10px]">✓</span>}
          </button>
        ))}
        {currentFolder && (
          <button
            type="button"
            onClick={() => pick("")}
            className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[13px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
          >
            Remove from folder
          </button>
        )}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const v = newName.trim();
            if (v) pick(v);
          }}
          className="mt-1 border-t border-[var(--border-subtle)] p-1.5"
        >
          <input
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder="New folder…"
            className="w-full rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 text-[13px] text-[var(--text-primary)] outline-none focus:border-[var(--accent)]"
          />
        </form>
      </PopoverContent>
    </Popover>
  );
}
