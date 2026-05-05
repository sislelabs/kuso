"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { useProject, useAddons } from "@/features/projects";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { ProjectCanvas } from "@/components/canvas/ProjectCanvas";
import { ServiceOverlay } from "@/components/service/ServiceOverlay";
import { AddonOverlay } from "@/components/addon/AddonOverlay";
import { Package } from "lucide-react";

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
  const [selectedAddon, setSelectedAddon] = useState<string | null>(null);

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
  const services = data.services;
  const allEnvs = data.environments;
  const addonsList = addons.data ?? [];

  // Narrow envs to the one the user picked in the TopNav. "production"
  // matches every env where spec.kind === "production"; everything else
  // matches a preview env by short name.
  const envs = allEnvs.filter((e) => {
    if (selectedEnv === "production") return e.spec.kind === "production";
    const short = e.metadata.name.split("-").slice(-2).join("-");
    return short === selectedEnv;
  });

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
      <div className="flex h-[calc(100vh-3rem)] flex-col overflow-hidden">
        <ProjectCanvas
          project={projectName}
          services={services}
          addons={addonsList}
          envs={envs}
          onSelectService={(shortName) => setSelectedService(shortName)}
          onSelectAddon={(name) => setSelectedAddon(name)}
        />
      </div>

      <ServiceOverlay
        project={projectName}
        service={selectedService}
        env={selectedEnv}
        onClose={() => setSelectedService(null)}
      />

      <AddonOverlay
        project={projectName}
        addon={selectedAddon}
        onClose={() => setSelectedAddon(null)}
      />
    </>
  );
}
