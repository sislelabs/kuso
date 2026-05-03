"use client";

import { useState } from "react";
import { useSearchParams } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { useProject, useAddons } from "@/features/projects";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { ProjectCanvas } from "@/components/canvas/ProjectCanvas";
import { ServiceOverlay } from "@/components/service/ServiceOverlay";
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
          title="No services or addons yet"
          description="Connect a repo to add a service. Right-click the canvas to add an addon. The graph lights up as soon as you do."
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
        />
      </div>

      <ServiceOverlay
        project={projectName}
        service={selectedService}
        env={selectedEnv}
        onClose={() => setSelectedService(null)}
      />
    </>
  );
}
