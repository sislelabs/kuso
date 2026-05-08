"use client";

import { useEffect, useState } from "react";
import dynamic from "next/dynamic";
import { useSearchParams } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { useProject, useAddons } from "@/features/projects";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { ServiceOverlay } from "@/components/service/ServiceOverlay";
import { AddonOverlay } from "@/components/addon/AddonOverlay";
import { MobileIncidentView } from "@/components/project/MobileIncidentView";
import { Package } from "lucide-react";

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

// ProjectDetailView is canvas-only. Project name + repo live in the
// TopNav breadcrumb; service interactions happen via right-click on
// canvas nodes (open / view logs / trigger build / delete) or by
// clicking a node which opens the ServiceOverlay panel.
export function ProjectDetailView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const projectName = params.project ?? "";
  const search = useSearchParams();

  // Overlay state — in-component, NOT in the URL. The panel is a
  // transient inspector, not a route.
  const [selectedService, setSelectedService] = useState<string | null>(null);
  const [selectedServiceTab, setSelectedServiceTab] = useState<string | undefined>(undefined);
  const [selectedAddon, setSelectedAddon] = useState<string | null>(null);
  const [selectedAddonTab, setSelectedAddonTab] = useState<string | undefined>(undefined);

  // Notification-link entry point: when the URL carries ?service= /
  // ?addon=, open the matching overlay on mount. Lets bell-icon
  // notifications navigate straight to the relevant resource (e.g.
  // build.succeeded → /projects/<p>?service=<s> → opens the
  // service overlay's Deployments tab). One-shot — we only honour
  // the param on first render so closing the overlay doesn't snap
  // back open on a re-render.
  //
  // Both notification storage shapes hit this entry point: the
  // notify package writes the SHORT service name on the URL, but
  // the legacy build poller used to write the FQ name. Strip the
  // "<project>-" prefix here so either form opens the right
  // overlay (the overlay queries by short name).
  useEffect(() => {
    const stripPrefix = (s: string) => {
      const p = projectName + "-";
      return s.startsWith(p) ? s.slice(p.length) : s;
    };
    const svc = search?.get("service");
    const addon = search?.get("addon");
    if (svc) setSelectedService(stripPrefix(svc));
    if (addon) setSelectedAddon(stripPrefix(addon));
    // Re-run on URL change so the cmd-K palette can navigate from
    // /projects/<p>?service=A to /projects/<p>?service=B by pushing
    // a new URL — without this, the overlay keeps showing A.
  }, [search, projectName]);

  // Env switcher legitimately changes what's on screen and survives
  // reload, so it stays in the URL as ?env=<short>.
  const selectedEnv = search?.get("env") ?? "production";

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

  if (project.isError) {
    return (
      <div className="p-6 lg:p-8">
        <Card>
          <CardContent className="p-6 text-sm text-red-500">
            Failed to load project: {project.error?.message}
          </CardContent>
        </Card>
      </div>
    );
  }

  const data = project.data!;
  const allServices = data.services;
  const allEnvs = data.environments;
  const allAddons = addons.data ?? [];

  // Narrow services + envs + addons to the picked env-group. Group
  // membership lives on the kuso.sislelabs.com/env label —
  // "production" matches services/addons with no label OR
  // label=production; any other value (staging, client-demo,
  // preview-pr-N) matches an exact label.
  const envLabel = "kuso.sislelabs.com/env";
  const inGroup = (labels: Record<string, string> | undefined) => {
    const v = labels?.[envLabel];
    if (selectedEnv === "production") return !v || v === "production";
    return v === selectedEnv;
  };
  const services = allServices.filter((s) => inGroup(s.metadata.labels));
  const envs = allEnvs.filter((e) => inGroup(e.metadata.labels));
  const addonsList = allAddons.filter((a) => inGroup(a.metadata.labels));
  const onProduction = selectedEnv === "production";

  if (services.length === 0 && addonsList.length === 0) {
    return (
      <div className="p-6 lg:p-8">
        <EmptyState
          icon={<Package className="h-5 w-5" />}
          title="Empty project"
          description="A project is a container for services. Add the first service from a GitHub repo to get this canvas lit up."
          action={
            <a
              href={`/projects/${encodeURIComponent(projectName)}/services/new`}
              className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3 text-xs font-medium text-[var(--btn-primary-fg)] shadow-[var(--shadow-sm)] transition-colors hover:bg-[var(--btn-primary-bg-hover)] hover:scale-[1.02]"
            >
              + Add service
            </a>
          }
        />
      </div>
    );
  }

  return (
    <>
      <MobileIncidentView
        project={projectName}
        services={services}
        envs={envs}
        onSelectService={(shortName, tab) => {
          setSelectedService(shortName);
          setSelectedServiceTab(tab);
        }}
      />
      <div className="hidden h-[calc(100vh-3rem)] flex-col overflow-hidden sm:flex">
        {!onProduction && (
          // Sticky banner above the canvas. Reminds the user that the
          // env they're looking at started as a clone — env vars copied
          // verbatim, addon refs rewritten only for "fresh" addons.
          // Click "Variables" on any service to review. Custom envs
          // get amber; preview-pr envs get blue so the user can tell
          // them apart at a glance.
          <NonProdBanner project={projectName} env={selectedEnv} />
        )}
        <ProjectCanvas
          project={projectName}
          services={services}
          addons={addonsList}
          envs={envs}
          onSelectService={(shortName, tab) => {
            setSelectedService(shortName);
            setSelectedServiceTab(tab);
          }}
          onSelectAddon={(name, tab) => {
            setSelectedAddon(name);
            setSelectedAddonTab(tab);
          }}
        />
      </div>

      <ServiceOverlay
        project={projectName}
        service={selectedService}
        env={selectedEnv}
        defaultTab={selectedServiceTab}
        onClose={() => {
          setSelectedService(null);
          setSelectedServiceTab(undefined);
        }}
      />

      <AddonOverlay
        project={projectName}
        addon={selectedAddon}
        defaultTab={selectedAddonTab}
        onClose={() => {
          setSelectedAddon(null);
          setSelectedAddonTab(undefined);
        }}
      />
    </>
  );
}

// NonProdBanner is shown above the canvas when the user is viewing a
// non-production env. The use case (per design): "I cloned production
// to send a client a review URL — but env vars carried over and
// secret-ref rewrites only apply to addons I picked 'fresh' for."
// The banner is the gentle reminder. Click-through scrolls the user
// to the env switcher if they wanted to switch back, or they can
// dismiss it for the rest of the session via sessionStorage.
function NonProdBanner({ project, env }: { project: string; env: string }) {
  const isPreview = env.startsWith("pr-") || env.startsWith("preview-");
  const cls = isPreview
    ? "border-blue-500/30 bg-blue-500/5 text-blue-200"
    : "border-amber-500/30 bg-amber-500/5 text-amber-200";
  const label = isPreview ? "preview env" : "non-production env";
  return (
    <div className={`shrink-0 border-b ${cls}`}>
      <div className="flex items-start gap-3 px-4 py-2 text-[12px]">
        <span className="mt-0.5 inline-flex h-4 items-center rounded border border-current px-1.5 font-mono text-[9px] uppercase tracking-widest">
          {label}
        </span>
        <p className="flex-1 leading-snug text-[var(--text-secondary)]">
          You&apos;re viewing{" "}
          <span className="font-mono text-[var(--text-primary)]">{env}</span> in{" "}
          <span className="font-mono">{project}</span>.{" "}
          {isPreview ? (
            <>This is a PR-driven preview env. Closes automatically when the PR is merged or closed.</>
          ) : (
            <>
              This env was cloned from production. Env vars were copied verbatim;{" "}
              <span className="font-medium">addon refs were rewritten only for &quot;fresh&quot; addons</span>.
              Open each service&apos;s <span className="font-mono">Variables</span> tab and confirm
              they point at the right secrets before sharing the URL.
            </>
          )}
        </p>
      </div>
    </div>
  );
}
