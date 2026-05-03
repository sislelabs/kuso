"use client";

import Link from "next/link";
import { useState } from "react";
import { useRouteParams } from "@/lib/dynamic-params";
import { useProject, useAddons } from "@/features/projects";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/shared/EmptyState";
import { DeployStatusPill, type DeployStatus } from "@/components/service/DeployStatusPill";
import { SleepBadge } from "@/components/service/SleepBadge";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { ProjectCanvas } from "@/components/canvas/ProjectCanvas";
import { ExternalLink, Plus, Package, Database, LayoutGrid, List } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { cn, serviceShortName } from "@/lib/utils";

function envForService(envs: KusoEnvironment[], svcName: string, kind = "production"): KusoEnvironment | undefined {
  return envs.find((e) => e.spec.service === svcName && e.spec.kind === kind);
}

function statusFor(env?: KusoEnvironment): DeployStatus {
  if (!env) return "unknown";
  const phase = (env.status?.phase ?? "").toString().toLowerCase();
  if (phase === "building") return "building";
  if (phase === "deploying") return "deploying";
  if (env.status?.ready) return "active";
  if (phase === "failed" || phase === "error") return "failed";
  if (phase === "sleeping") return "sleeping";
  return "unknown";
}

function ServiceCard({
  service,
  envs,
  project,
}: {
  service: KusoService;
  envs: KusoEnvironment[];
  project: string;
}) {
  // metadata.name is the FQN ("<project>-<short>"). API URLs + the UI's
  // user-facing name use the SHORT form. envForService still matches on
  // the FQN because that's what spec.service stores.
  const env = envForService(envs, service.metadata.name);
  const status = statusFor(env);
  const shortName = serviceShortName(project, service.metadata.name);
  return (
    <Link href={`/projects/${project}/services/${shortName}`} className="block">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center justify-between">
            <span className="flex items-center gap-2 truncate">
              <RuntimeIcon runtime={service.spec.runtime} />
              <span className="truncate">{shortName}</span>
            </span>
            <DeployStatusPill status={status} />
          </CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="space-y-1.5 text-xs">
            {service.spec.runtime && (
              <div className="flex items-center gap-2">
                <dt className="font-mono text-[var(--text-tertiary)]">runtime</dt>
                <dd className="font-mono text-[var(--text-secondary)]">
                  {service.spec.runtime}
                </dd>
              </div>
            )}
            {service.spec.port !== undefined && (
              <div className="flex items-center gap-2">
                <dt className="font-mono text-[var(--text-tertiary)]">port</dt>
                <dd className="font-mono text-[var(--text-secondary)]">
                  {service.spec.port}
                </dd>
              </div>
            )}
            {env?.status?.url && (
              <div className="flex items-center gap-2">
                <dt className="font-mono text-[var(--text-tertiary)]">url</dt>
                <dd className="truncate">
                  <span className="font-mono text-[var(--accent)] truncate inline-flex items-center gap-1">
                    {(env.status.url as string).replace(/^https?:\/\//, "")}
                    <ExternalLink className="h-3 w-3 shrink-0" />
                  </span>
                </dd>
              </div>
            )}
          </dl>
          {status === "sleeping" && <SleepBadge className="mt-3" />}
        </CardContent>
      </Card>
    </Link>
  );
}

export function ProjectDetailView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const projectName = params.project ?? "";
  const [view, setView] = useState<"canvas" | "list">("canvas");

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
  const envs = data.environments;
  const addonsList = addons.data ?? [];

  // Empty project: skip the canvas and show a centered CTA.
  if (services.length === 0 && addonsList.length === 0) {
    return (
      <div className="p-6 lg:p-8">
        <div className="mb-6">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">{projectName}</h1>
          {data.project.spec.description && (
            <p className="mt-1 text-sm text-[var(--text-secondary)]">
              {data.project.spec.description}
            </p>
          )}
        </div>
        <EmptyState
          icon={<Package className="h-5 w-5" />}
          title="No services or addons yet"
          description="Add a service from your repo, then attach a Postgres or Redis addon. The canvas lights up as soon as you do."
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col h-[calc(100vh-3.5rem)] overflow-hidden">
      {/* Toolbar — h-11 to keep canvas math predictable. */}
      <div className="flex h-11 shrink-0 items-center justify-between gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 lg:px-6">
        <div className="min-w-0">
          <h1 className="truncate font-heading text-base font-semibold tracking-tight">
            {projectName}
          </h1>
          {data.project.spec.defaultRepo?.url && (
            <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
              {data.project.spec.defaultRepo.url.replace(/^https?:\/\/(www\.)?/, "")}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2">
          <div className="inline-flex rounded-md border border-[var(--border-subtle)] p-0.5 text-xs">
            <button
              type="button"
              onClick={() => setView("canvas")}
              className={cn(
                "inline-flex items-center gap-1.5 rounded px-2 py-1",
                view === "canvas"
                  ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                  : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
              )}
            >
              <LayoutGrid className="h-3 w-3" /> Canvas
            </button>
            <button
              type="button"
              onClick={() => setView("list")}
              className={cn(
                "inline-flex items-center gap-1.5 rounded px-2 py-1",
                view === "list"
                  ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                  : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
              )}
            >
              <List className="h-3 w-3" /> List
            </button>
          </div>
          <Link
            href={`/projects/${projectName}/settings`}
            className="text-xs text-[var(--text-secondary)] underline"
          >
            settings
          </Link>
        </div>
      </div>

      {view === "canvas" ? (
        <ProjectCanvas
          project={projectName}
          services={services}
          addons={addonsList}
          envs={envs}
        />
      ) : (
        <div className="mx-auto w-full max-w-6xl p-6 lg:p-8">
          <section className="mb-8">
            <div className="mb-3 flex items-center justify-between">
              <h2 className="font-heading text-lg font-semibold">Services</h2>
              <Button variant="outline" size="sm" disabled>
                <Plus className="h-3.5 w-3.5" /> Add service
              </Button>
            </div>
            {services.length === 0 ? (
              <EmptyState
                icon={<Package className="h-5 w-5" />}
                title="No services yet"
                description="A service is one deployable process."
              />
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                {services.map((s) => (
                  <ServiceCard
                    key={s.metadata.uid ?? s.metadata.name}
                    service={s}
                    envs={envs}
                    project={projectName}
                  />
                ))}
              </div>
            )}
          </section>

          <section>
            <div className="mb-3 flex items-center justify-between">
              <h2 className="font-heading text-lg font-semibold">Addons</h2>
              <Button variant="outline" size="sm" disabled>
                <Plus className="h-3.5 w-3.5" /> Add addon
              </Button>
            </div>
            {addonsList.length === 0 ? (
              <EmptyState
                icon={<Database className="h-5 w-5" />}
                title="No addons yet"
                description="Postgres, Redis, MongoDB — pick one and the connection env vars are wired into every service."
              />
            ) : (
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {addonsList.map((a) => (
                  <Card key={a.metadata.uid ?? a.metadata.name} size="sm">
                    <CardHeader>
                      <CardTitle className="flex items-center gap-2">
                        <AddonIcon kind={a.spec.kind} />
                        <span className="truncate">{a.metadata.name}</span>
                      </CardTitle>
                    </CardHeader>
                    <CardContent>
                      <dl className="space-y-1 text-xs">
                        <div className="flex items-center gap-2">
                          <dt className="font-mono text-[var(--text-tertiary)]">kind</dt>
                          <dd className="font-mono text-[var(--text-secondary)]">
                            {addonLabel(a.spec.kind)}
                          </dd>
                        </div>
                        {a.spec.version && (
                          <div className="flex items-center gap-2">
                            <dt className="font-mono text-[var(--text-tertiary)]">version</dt>
                            <dd className="font-mono text-[var(--text-secondary)]">
                              {a.spec.version}
                            </dd>
                          </div>
                        )}
                        {a.status?.connectionSecret && (
                          <div className="flex items-center gap-2">
                            <dt className="font-mono text-[var(--text-tertiary)]">secret</dt>
                            <dd className="truncate font-mono text-[var(--text-secondary)]">
                              {a.status.connectionSecret}
                            </dd>
                          </div>
                        )}
                      </dl>
                    </CardContent>
                  </Card>
                ))}
              </div>
            )}
          </section>
        </div>
      )}
    </div>
  );
}
