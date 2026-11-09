"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import dynamic from "next/dynamic";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { useProject, useAddons } from "@/features/projects";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { ApiError } from "@/lib/api-client";
import { ServiceOverlay } from "@/components/service/ServiceOverlay";
import { AddonOverlay } from "@/components/addon/AddonOverlay";
import { AddAddonDialog } from "@/components/addon/AddAddonDialog";
import { MobileIncidentView } from "@/components/project/MobileIncidentView";
import { ProjectStatusStrip } from "@/components/project/ProjectStatusStrip";
import { TopNavPortal } from "@/components/layout/TopNav";
import { EmptyProjectState } from "@/components/project/EmptyProjectState";
import { ProjectNotFound } from "@/components/project/ProjectNotFound";
import { ProjectListView } from "@/components/project/ProjectListView";
import { FirstDeployChecklist } from "@/components/project/FirstDeployChecklist";
import { useMediaQuery } from "@/components/project/useMediaQuery";
import {
  loadProjectView,
  saveProjectView,
  type ProjectViewMode,
} from "@/components/project/projectViewPref";
import { Package } from "lucide-react";
import { addonPageHref, servicePageHref } from "@/components/service/workspace/hrefs";
import { opensInPage } from "@/components/service/workspace/peekPref";
import { StagedChangesBar } from "@/components/staged";

// ReactFlow touches `window` and `ResizeObserver` at module scope, which
// blew up the static export build. ssr:false skips the prerender pass
// and only mounts the canvas on the client. The skeleton fills the same
// box during the first paint so the layout doesn't jump.
const ProjectCanvas = dynamic(
  () => import("@/components/canvas/ProjectCanvas").then((m) => m.ProjectCanvas),
  {
    ssr: false,
    loading: () => <Skeleton className="h-[600px] w-full" />,
  },
);

// Search params that describe the open overlay; dropped together on close.
const OVERLAY_PARAMS = ["service", "addon", "tab", "kind", "highlight"] as const;

// ProjectDetailView is canvas-only. Project name + repo live in the
// TopNav breadcrumb; service interactions happen via right-click on
// canvas nodes (open / view logs / trigger build / delete) or by
// clicking a node which opens the ServiceOverlay panel.
export function ProjectDetailView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const projectName = params.project ?? "";
  const search = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  // Overlay state lives in the URL: ?service=<s>&tab=<t> or
  // ?addon=<a>&tab=<t>. Opening pushes a history entry so Back closes the
  // overlay; tab switches replace it; reload and shared links restore
  // exactly what was on screen. The bell and ⌘K palette open overlays the
  // same way, by navigating to the URL.
  //
  // Notifications have written both the short and the "<project>-" FQ
  // service name over time; the overlay queries by short name.
  const stripPrefix = (s: string) => {
    const p = projectName + "-";
    return s.startsWith(p) ? s.slice(p.length) : s;
  };
  const urlService = search?.get("service");
  const urlAddon = search?.get("addon");
  const selectedService = urlService ? stripPrefix(urlService) : null;
  // The addon overlay addresses addons by full CR name (<project>-<addon>).
  const selectedAddon = !selectedService && urlAddon
    ? urlAddon.startsWith(projectName + "-") ? urlAddon : `${projectName}-${urlAddon}`
    : null;
  const selectedTab = search?.get("tab") ?? undefined;
  // ?kind=<failures.Kind> from a bell failure notification: the overlay
  // shows a FailureBanner on the routed tab until the user switches tabs.
  const failureKind = selectedService ? (search?.get("kind") ?? undefined) : undefined;

  // The URL (minus overlay params) we pushed the overlay on top of. Close
  // goes Back to it when nothing else changed since, so the history stack
  // doesn't grow a duplicate entry per open/close.
  const pushedFrom = useRef<string | null>(null);
  const urlWithout = (params: URLSearchParams) => {
    const next = new URLSearchParams(params.toString());
    for (const k of OVERLAY_PARAMS) next.delete(k);
    const qs = next.toString();
    return qs ? `${pathname}?${qs}` : pathname;
  };
  const navigate = (mutate: (p: URLSearchParams) => void, mode: "push" | "replace") => {
    const next = new URLSearchParams(search?.toString() ?? "");
    mutate(next);
    const qs = next.toString();
    const url = qs ? `${pathname}?${qs}` : pathname;
    if (mode === "push") router.push(url, { scroll: false });
    else router.replace(url, { scroll: false });
  };
  const openOverlay = (key: "service" | "addon", name: string, tab?: string) => {
    const current = new URLSearchParams(search?.toString() ?? "");
    const wasOpen = current.has("service") || current.has("addon");
    if (!wasOpen) pushedFrom.current = urlWithout(current);
    navigate((p) => {
      for (const k of OVERLAY_PARAMS) p.delete(k);
      p.set(key, name);
      if (tab) p.set("tab", tab);
    }, wasOpen ? "replace" : "push");
  };
  const closeOverlay = () => {
    const current = new URLSearchParams(search?.toString() ?? "");
    if (!current.has("service") && !current.has("addon")) return;
    if (pushedFrom.current !== null && pushedFrom.current === urlWithout(current)) {
      pushedFrom.current = null;
      router.back();
      return;
    }
    pushedFrom.current = null;
    navigate((p) => {
      for (const k of OVERLAY_PARAMS) p.delete(k);
    }, "replace");
  };
  const changeTab = (tab: string) => {
    if (search?.get("tab") === tab) return;
    navigate((p) => {
      p.set("tab", tab);
      // A tab switch dismisses the failure banner and any highlight.
      p.delete("kind");
      p.delete("highlight");
    }, "replace");
  };

  // Add-addon dialog: opened from the empty-state CTA so a user who
  // wants to start with a managed DB doesn't need to discover the
  // canvas right-click menu. Closed by AddAddonDialog on success.
  const [addAddonOpen, setAddAddonOpen] = useState(false);

  // Canvas vs list: ?view= wins (shareable), else the last explicit
  // choice for this project in this browser, else list below ~900px
  // (a canvas that narrow is mostly panning) and canvas above.
  // Lazy init avoids a canvas flash before the effect runs; the first
  // render is the loading skeleton either way, so no hydration mismatch.
  const [storedView, setStoredView] = useState<ProjectViewMode | null>(() => loadProjectView(projectName));
  useEffect(() => {
    setStoredView(loadProjectView(projectName));
  }, [projectName]);
  const narrow = useMediaQuery("(max-width: 899px)");
  const urlView = search?.get("view");
  const viewMode: ProjectViewMode =
    // Narrow screens open on the list; the stored choice is a desktop
    // preference (a canvas that narrow is mostly panning).
    urlView === "list" || urlView === "canvas" ? urlView : narrow ? "list" : (storedView ?? "canvas");
  const changeViewMode = (m: ProjectViewMode) => {
    setStoredView(m);
    saveProjectView(projectName, m);
    const next = new URLSearchParams(search?.toString() ?? "");
    // The URL carries whichever view differs from this width's default.
    if (m === (narrow ? "canvas" : "list")) next.set("view", m);
    else next.delete("view");
    const qs = next.toString();
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false });
  };

  // Env switcher legitimately changes what's on screen and survives
  // reload, so it stays in the URL as ?env=<short>.
  const selectedEnv = search?.get("env") ?? "production";

  // Below sm there is no peek: the full page is the mobile view.
  const phone = useMediaQuery("(max-width: 639px)");
  const openServicePage = useCallback(
    (name: string, tab?: string) => router.push(servicePageHref(projectName, name, { env: selectedEnv, tab })),
    [router, projectName, selectedEnv],
  );
  const openAddonPage = useCallback(
    (name: string, tab?: string) => router.push(addonPageHref(projectName, name, { env: selectedEnv, tab })),
    [router, projectName, selectedEnv],
  );
  // An old ?service=/?addon= link opened on a phone lands on the page.
  useEffect(() => {
    if (!phone || !projectName) return;
    const tab = search?.get("tab") ?? undefined;
    if (selectedService) {
      const q = new URLSearchParams();
      for (const k of ["kind", "highlight"] as const) {
        const v = search?.get(k);
        if (v) q.set(k, v);
      }
      const base = servicePageHref(projectName, selectedService, { env: selectedEnv, tab });
      const extra = q.toString();
      router.replace(extra ? `${base}${base.includes("?") ? "&" : "?"}${extra}` : base);
    } else if (selectedAddon) {
      router.replace(addonPageHref(projectName, selectedAddon, { env: selectedEnv, tab }));
    }
  }, [phone, projectName, selectedService, selectedAddon, selectedEnv, search, router]);

  // Double-click (or ⌘/Ctrl+Enter) on a canvas node or list row opens
  // the full page; a single click keeps the peek. Capture phase, so it
  // runs before React Flow's own double-click zoom.
  const surfaceRef = useRef<HTMLDivElement>(null);
  const pageRef = useRef({ openServicePage, openAddonPage, projectName });
  pageRef.current = { openServicePage, openAddonPage, projectName };
  useEffect(() => {
    const el = surfaceRef.current;
    if (!el) return;
    const openFrom = (target: EventTarget | null): boolean => {
      const node = target instanceof Element ? target.closest<HTMLElement>("[data-focus-key]") : null;
      const key = node?.dataset.focusKey;
      if (!key) return false;
      const sep = key.indexOf(":");
      const kind = key.slice(0, sep);
      const name = key.slice(sep + 1);
      const { openServicePage: svc, openAddonPage: addon, projectName: p } = pageRef.current;
      if (kind === "service") svc(name);
      else if (kind === "addon") addon(name.startsWith(`${p}-`) ? name.slice(p.length + 1) : name);
      else return false;
      return true;
    };
    const onDbl = (e: MouseEvent) => {
      if (openFrom(e.target)) {
        e.preventDefault();
        e.stopPropagation();
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
      if (openFrom(e.target)) {
        e.preventDefault();
        e.stopPropagation();
      }
    };
    el.addEventListener("dblclick", onDbl, true);
    el.addEventListener("keydown", onKey, true);
    return () => {
      el.removeEventListener("dblclick", onDbl, true);
      el.removeEventListener("keydown", onKey, true);
    };
  });

  const project = useProject(projectName);
  const addons = useAddons(projectName);

  if (project.isPending || addons.isPending) {
    return (
      <div className="p-6 lg:p-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (project.isError && project.error instanceof ApiError && project.error.status === 404) {
    return <ProjectNotFound project={projectName} />;
  }

  if (project.isError) {
    return (
      <div className="p-6 lg:p-8">
        <Card>
          <CardContent className="space-y-3 p-6 text-sm">
            <p className="text-[var(--error)]">Failed to load project: {project.error?.message}</p>
            <div className="flex items-center gap-2">
              <Button type="button" variant="outline" size="sm" onClick={() => void project.refetch()}>
                Retry
              </Button>
              <Link href="/projects" className="text-xs text-[var(--text-secondary)] underline-offset-2 hover:underline">
                Back to projects
              </Link>
            </div>
          </CardContent>
        </Card>
      </div>
    );
  }

  const data = project.data!;
  const allServices = data.services;
  const allEnvs = data.environments;
  const allAddons = addons.data ?? [];

  // Narrow envs + addons to the picked env-group, but show every
  // service. Group membership lives on the kuso.sislelabs.com/env
  // label — "production" matches with no label OR label=production;
  // any other value (staging, client-demo, preview-pr-N) matches
  // exactly.
  //
  // Two service models coexist:
  //  A) A shared, env-independent KusoService (one CR, referenced by N
  //     KusoEnvironments) carries NO env label — it belongs to EVERY
  //     env-group. Filtering it out on non-production tabs was the old
  //     "Empty project" bug; inGroup keeps it (label absent -> matches).
  //  B) An env-group CLONE creates a SEPARATE KusoService CR labeled
  //     kuso.sislelabs.com/env=<group> — it belongs ONLY to that group.
  //     Not filtering these was the "duplicate node in production" bug
  //     (the staging clone's service leaked into the production canvas).
  // svcInGroup handles both: no label -> shown everywhere; a label ->
  // shown only on its own env tab (clone services carry the label as of
  // the env-group service-label fix).
  const envLabel = "kuso.sislelabs.com/env";
  // inGroup is the ENV-CR predicate: env CRs always carry the group
  // label (unlabeled = legacy production), so "no label" only matches
  // the production tab. Do NOT use it for services — see svcInGroup.
  const inGroup = (labels: Record<string, string> | undefined) => {
    const v = labels?.[envLabel];
    if (selectedEnv === "production") return !v || v === "production";
    return v === selectedEnv;
  };
  // svcInGroup is the SERVICE predicate: a shared KusoService carries
  // NO env label and belongs to EVERY env-group (model A above), so
  // "no label" matches every tab; an env-group clone's labeled service
  // (model B) matches only its own tab. Running services through
  // inGroup instead was the v0.21.x regression that emptied every
  // non-production canvas (staging + preview-pr-N showed addons only:
  // inGroup returns v === selectedEnv, false when v is undefined).
  const svcInGroup = (labels: Record<string, string> | undefined) => {
    const v = labels?.[envLabel];
    if (!v) return true;
    if (selectedEnv === "production") return v === "production";
    return v === selectedEnv;
  };
  const envs = allEnvs.filter((e) => inGroup(e.metadata.labels));
  // Addon visibility per env-group. Two cases:
  //
  //  - An addon explicitly scoped to one env (label
  //    kuso.sislelabs.com/env=staging) is the "true isolation" case — it
  //    renders ONLY under its own tab.
  //  - An unlabeled addon is project-shared: its conn-secret is injected into
  //    every env, so it normally shows under every tab. BUT when the selected
  //    env has its OWN clone of that base (e.g. staging mounts
  //    scubatony-db-staging instead of the shared scubatony-db — per-env addon
  //    provisioning), the shared base must be HIDDEN on that tab so the canvas
  //    matches what the env's services actually mount. Production (and any env
  //    without a clone of a given base) still shows the shared base.
  //
  // replacedBases = the base names this env has an env-scoped clone for.
  // Custom env-group clones are named "<base>-<selectedEnv>" (staging →
  // scubatony-db-staging), but PREVIEW clones use the SHORT pr suffix:
  // env-group "preview-pr-46" names its clones "<base>-pr-46" (server-
  // side previewdb naming). Strip whichever suffix matches — deriving
  // only "-<selectedEnv>" left replacedBases empty on preview tabs, so
  // the shared bases rendered alongside their pr-N clones.
  const cloneSuffixes = [`-${selectedEnv}`];
  if (selectedEnv.startsWith("preview-")) {
    cloneSuffixes.push(`-${selectedEnv.slice("preview-".length)}`);
  }
  const replacedBases = new Set(
    selectedEnv !== "production"
      ? allAddons
          .filter((a) => a.metadata.labels?.[envLabel] === selectedEnv)
          .map((a) => a.metadata.name)
          .flatMap((n) => {
            const suffix = cloneSuffixes.find((s) => n.endsWith(s));
            return suffix ? [n.slice(0, -suffix.length)] : [];
          })
      : [],
  );
  // When the selected env exists at all (i.e. has at least one env
  // CR or env-scoped addon), show every service so the canvas
  // matches the shape of the project. If the env-group has zero
  // envs AND zero env-scoped addons, treat the project as empty in
  // that env. Project-shared addons alone don't constitute an env —
  // a service-less env-group is still empty from the user's POV.
  const envExists =
    envs.length > 0 ||
    allAddons.some((a) => a.metadata.labels?.[envLabel] === selectedEnv) ||
    // Env-labeled service CRs count too: a clone group whose env CRs
    // are mid-teardown but whose labeled service CR survives is still
    // a live thing to show, not an "Environment not found".
    allServices.some((s) => s.metadata.labels?.[envLabel] === selectedEnv);
  const onProduction = selectedEnv === "production";
  // Addons are gated on envExists like services below: a stale ?env=
  // param (a preview torn down while the tab was open) must render the
  // empty state, not a ghost canvas of every project-shared addon with
  // zero services. Production always exists.
  const addonsList =
    onProduction || envExists
      ? allAddons.filter((a) => {
          const v = a.metadata.labels?.[envLabel];
          if (!v) return !replacedBases.has(a.metadata.name); // shared: hide if this env clones it
          return v === selectedEnv; // env-scoped: only on its own tab
        })
      : [];
  // Services replaced by a clone in THIS env, mirroring replacedBases
  // for addons. Without it a base service rendered as a ghost on its
  // clone's tab — two nodes both titled by the (cloned-verbatim)
  // displayName, the ghost showing production's domain chip (the
  // bukvite staging confusion).
  const replacedBaseServices = new Set(
    selectedEnv !== "production"
      ? allServices
          .filter((s) => s.metadata.labels?.[envLabel] === selectedEnv)
          .map((s) => s.metadata.name)
          .flatMap((n) => {
            const suffix = cloneSuffixes.find((sfx) => n.endsWith(sfx));
            return suffix ? [n.slice(0, -suffix.length)] : [];
          })
      : [],
  );
  // Filter services to the selected env-group: shared (no env label)
  // services show on every tab UNLESS this env has a clone of them; a
  // clone's env-labeled service shows only on its own tab (fixes the
  // duplicate node bleeding into production).
  const services = envExists
    ? allServices.filter((s) => {
        if (!svcInGroup(s.metadata.labels)) return false;
        const v = s.metadata.labels?.[envLabel];
        if (!v) return !replacedBaseServices.has(s.metadata.name); // shared: hide if this env clones it
        return true;
      })
    : [];

  // Stale ?env= (a preview torn down while the tab was open, a typo'd
  // URL): the generic "Empty project" copy would be false and — with
  // no env tabs rendered on empty states — a dead end. Name the
  // problem and hand back the way out.
  if (!onProduction && !envExists) {
    return (
      <div className="p-6 lg:p-8">
        <EmptyState
          icon={<Package className="h-5 w-5" />}
          title={`Environment "${selectedEnv}" not found`}
          description="This environment doesn't exist (anymore) — a preview environment may have been torn down after its PR closed or expired."
          action={
            <Link
              href={`/projects/${encodeURIComponent(projectName)}`}
              className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] shadow-[var(--shadow-sm)] transition-colors hover:bg-[var(--btn-primary-bg-hover)] hover:scale-[1.02]"
            >
              Back to production
            </Link>
          }
        />
      </div>
    );
  }

  if (services.length === 0 && addonsList.length === 0) {
    return (
      <>
        <EmptyProjectState project={projectName} onAddDatabase={() => setAddAddonOpen(true)} />
        <AddAddonDialog
          project={projectName}
          open={addAddonOpen}
          onClose={() => setAddAddonOpen(false)}
        />
      </>
    );
  }

  // Settings and Logs skip the peek for the full page (peekPref).
  const selectService = (shortName: string, tab?: string) =>
    phone || opensInPage(tab) ? openServicePage(shortName, tab) : openOverlay("service", shortName, tab);
  const selectAddon = (name: string, tab?: string) =>
    phone ? openAddonPage(stripPrefix(name), tab) : openOverlay("addon", stripPrefix(name), tab);

  return (
    <>
      <MobileIncidentView
        project={projectName}
        selectedEnv={selectedEnv}
        services={services}
        addons={addonsList}
        envs={envs}
        onSelectService={selectService}
        onSelectAddon={selectAddon}
      />
      <StagedChangesBar project={projectName} />
      <div ref={surfaceRef} className="relative hidden h-[calc(100vh-3rem)] flex-col overflow-hidden sm:flex">
        {addons.isError && (
          <div
            role="alert"
            className="shrink-0 border-b border-[var(--error)]/30 bg-[var(--error-subtle)] px-6 py-2 text-xs text-[var(--error)]"
          >
            Couldn&apos;t load addons, so databases are missing from this canvas: {addons.error?.message}{" "}
            <button type="button" className="underline underline-offset-2" onClick={() => void addons.refetch()}>
              Retry
            </button>
          </div>
        )}
        <FirstDeployChecklist
          project={projectName}
          selectedEnv={selectedEnv}
          services={services}
          envs={envs}
          onOpenService={selectService}
        />
        {/* The view toggle, stats and log search live in the top bar;
            the non-production reminder is the chip by the env switcher. */}
        <TopNavPortal>
          <ProjectStatusStrip
            project={projectName}
            selectedEnv={selectedEnv}
            services={services}
            envs={envs}
            view={viewMode}
            onViewChange={changeViewMode}
            onOpenService={selectService}
          />
        </TopNavPortal>
        {viewMode === "list" ? (
          <ProjectListView
            project={projectName}
            selectedEnv={selectedEnv}
            services={services}
            addons={addonsList}
            envs={envs}
            onSelectService={selectService}
            onSelectAddon={selectAddon}
          />
        ) : (
          <ProjectCanvas
            project={projectName}
            selectedEnv={selectedEnv}
            services={services}
            addons={addonsList}
            envs={envs}
            onSelectService={selectService}
            onSelectAddon={selectAddon}
            selectedNodeId={
              selectedService
                ? `svc:${projectName}-${selectedService}`
                : selectedAddon
                  ? `addon:${selectedAddon}`
                  : null
            }
            onClearSelection={closeOverlay}
          />
        )}
      </div>

      <ServiceOverlay
        project={projectName}
        service={selectedService}
        env={selectedEnv}
        defaultTab={selectedService ? selectedTab : undefined}
        failureKind={failureKind}
        onTabChange={changeTab}
        onRestore={(s) => selectService(s)}
        onOpenPage={(s, tab) => openServicePage(s, tab)}
        onClose={closeOverlay}
      />

      <AddonOverlay
        project={projectName}
        addon={selectedAddon}
        defaultTab={selectedAddon ? selectedTab : undefined}
        onTabChange={changeTab}
        onRestore={(a) => selectAddon(a)}
        onOpenPage={(a, tab) => openAddonPage(stripPrefix(a), tab)}
        onClose={closeOverlay}
      />
    </>
  );
}
