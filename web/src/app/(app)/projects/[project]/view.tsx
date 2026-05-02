"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useProject, useAddons } from "@/features/projects";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/shared/EmptyState";
import { DeployStatusPill, type DeployStatus } from "@/components/service/DeployStatusPill";
import { SleepBadge } from "@/components/service/SleepBadge";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { ExternalLink, Plus, Package, Database } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";

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
}: {
  service: KusoService;
  envs: KusoEnvironment[];
}) {
  const env = envForService(envs, service.metadata.name);
  const status = statusFor(env);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center justify-between">
          <span className="flex items-center gap-2 truncate">
            <RuntimeIcon runtime={service.spec.runtime} />
            <span className="truncate">{service.metadata.name}</span>
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
                <a
                  href={env.status.url as string}
                  target="_blank"
                  rel="noreferrer"
                  className="font-mono text-[var(--accent)] hover:underline truncate inline-flex items-center gap-1"
                >
                  {(env.status.url as string).replace(/^https?:\/\//, "")}
                  <ExternalLink className="h-3 w-3 shrink-0" />
                </a>
              </dd>
            </div>
          )}
          {env?.status?.commit && (
            <div className="flex items-center gap-2">
              <dt className="font-mono text-[var(--text-tertiary)]">commit</dt>
              <dd className="font-mono text-[var(--text-secondary)] truncate">
                {(env.status.commit as string).slice(0, 7)}
              </dd>
            </div>
          )}
        </dl>
        {status === "sleeping" && <SleepBadge className="mt-3" />}
      </CardContent>
    </Card>
  );
}

export function ProjectDetailView() {
  const params = useParams<{ project: string }>();
  const projectName = params?.project ?? "";

  const project = useProject(projectName);
  const addons = useAddons(projectName);

  if (project.isPending || addons.isPending) {
    return (
      <div className="p-6 lg:p-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <div className="grid gap-4 sm:grid-cols-2">
          <Skeleton className="h-32" />
          <Skeleton className="h-32" />
        </div>
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

  return (
    <div className="mx-auto max-w-6xl p-6 lg:p-8">
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">
            {projectName}
          </h1>
          {data.project.spec.description && (
            <p className="mt-1 text-sm text-[var(--text-secondary)]">
              {data.project.spec.description}
            </p>
          )}
          {data.project.spec.defaultRepo?.url && (
            <p className="mt-1 font-mono text-xs text-[var(--text-tertiary)]">
              {data.project.spec.defaultRepo.url}
            </p>
          )}
        </div>
        <Link
          href={`/projects/${projectName}/settings`}
          className="text-sm text-[var(--text-secondary)] underline"
        >
          settings →
        </Link>
      </div>

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
            description="A service is one deployable process. Add one from your repo to get started."
          />
        ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            {services.map((s) => (
              <Link
                key={s.metadata.uid ?? s.metadata.name}
                href={`/projects/${projectName}/services/${s.metadata.name}`}
                className="block"
              >
                <ServiceCard service={s} envs={envs} />
              </Link>
            ))}
          </div>
        )}
      </section>

      <section className="mb-8">
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
            description="Postgres, Redis, MongoDB, MySQL — pick one and the connection env vars are wired into every service in this project."
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
  );
}
