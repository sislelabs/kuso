"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { DeployStatusPill } from "@/components/service/DeployStatusPill";
import { RuntimeIcon } from "@/components/service/RuntimeIcon";
import { EnvVarsEditor } from "@/components/service/EnvVarsEditor";
import { useService, useTriggerBuild, useBuilds } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { LogStream } from "@/components/logs/LogStream";
import { ChevronLeft, RotateCcw, ExternalLink } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { toast } from "sonner";

export function ServiceDetailView() {
  const params = useParams<{ project: string; service: string }>();
  const project = params?.project ?? "";
  const service = params?.service ?? "";
  const [tab, setTab] = useState<"overview" | "env" | "builds" | "logs">("overview");

  const svc = useService(project, service);
  const envs = useEnvironments(project);
  const builds = useBuilds(project, service);
  const trigger = useTriggerBuild(project, service);

  const env = (envs.data ?? []).find(
    (e) => e.spec.service === service && e.spec.kind === "production"
  );

  const onRedeploy = async () => {
    try {
      await trigger.mutateAsync({});
      toast.success("Build triggered");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to trigger build");
    }
  };

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <Link
        href={`/projects/${project}`}
        className="inline-flex items-center gap-1 text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
      >
        <ChevronLeft className="h-3 w-3" />
        {project}
      </Link>

      {svc.isPending ? (
        <Skeleton className="mt-4 h-8 w-64" />
      ) : svc.isError ? (
        <p className="mt-4 text-sm text-red-500">
          Failed to load service: {svc.error?.message}
        </p>
      ) : (
        <>
          <div className="mt-3 flex items-center justify-between gap-4">
            <h1 className="font-heading text-2xl font-semibold tracking-tight inline-flex items-center gap-3">
              <RuntimeIcon runtime={svc.data?.spec.runtime} />
              {service}
            </h1>
            <div className="flex items-center gap-3">
              {env && (
                <DeployStatusPill
                  status={
                    env.status?.phase === "building"
                      ? "building"
                      : env.status?.ready
                        ? "active"
                        : "unknown"
                  }
                />
              )}
              <Button onClick={onRedeploy} disabled={trigger.isPending} size="sm">
                <RotateCcw className="h-3.5 w-3.5" />
                {trigger.isPending ? "Deploying…" : "Redeploy"}
              </Button>
            </div>
          </div>
          {env?.status?.url && (
            <p className="mt-2">
              <a
                href={env.status.url as string}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 font-mono text-xs text-[var(--accent)] hover:underline"
              >
                {(env.status.url as string).replace(/^https?:\/\//, "")}
                <ExternalLink className="h-3 w-3" />
              </a>
            </p>
          )}

          <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)} className="mt-6">
            <TabsList>
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="env">Env vars</TabsTrigger>
              <TabsTrigger value="builds">Builds</TabsTrigger>
              <TabsTrigger value="logs">Logs</TabsTrigger>
            </TabsList>

            <TabsContent value="overview" className="mt-4">
              <Card>
                <CardContent className="p-6">
                  <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
                    {svc.data?.spec.runtime && (
                      <div>
                        <dt className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          runtime
                        </dt>
                        <dd className="font-mono">{svc.data.spec.runtime}</dd>
                      </div>
                    )}
                    {svc.data?.spec.port !== undefined && (
                      <div>
                        <dt className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          port
                        </dt>
                        <dd className="font-mono">{svc.data.spec.port}</dd>
                      </div>
                    )}
                    {svc.data?.spec.scale && (
                      <div>
                        <dt className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          scale
                        </dt>
                        <dd className="font-mono">
                          {svc.data.spec.scale.min ?? 1}–{svc.data.spec.scale.max ?? 1}
                          {" replicas"}
                        </dd>
                      </div>
                    )}
                    {svc.data?.spec.repo?.url && (
                      <div>
                        <dt className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          repo
                        </dt>
                        <dd className="font-mono truncate">{svc.data.spec.repo.url}</dd>
                      </div>
                    )}
                    {svc.data?.spec.repo?.path && (
                      <div>
                        <dt className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                          path
                        </dt>
                        <dd className="font-mono">{svc.data.spec.repo.path}</dd>
                      </div>
                    )}
                  </dl>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="env" className="mt-4">
              <Card>
                <CardHeader>
                  <CardTitle>Environment variables</CardTitle>
                </CardHeader>
                <CardContent>
                  <EnvVarsEditor project={project} service={service} />
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="builds" className="mt-4">
              <Card>
                <CardHeader>
                  <CardTitle>Recent builds</CardTitle>
                </CardHeader>
                <CardContent>
                  {builds.isPending && <p className="text-xs">loading…</p>}
                  {builds.data && builds.data.length === 0 && (
                    <p className="text-xs text-[var(--text-tertiary)]">No builds yet.</p>
                  )}
                  {builds.data && builds.data.length > 0 && (
                    <ul className="divide-y divide-[var(--border-subtle)]">
                      {builds.data.map((b) => (
                        <li key={b.id} className="flex items-center justify-between py-2">
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <DeployStatusPill
                                status={
                                  b.status === "succeeded"
                                    ? "active"
                                    : b.status === "failed"
                                      ? "failed"
                                      : b.status === "building"
                                        ? "building"
                                        : "unknown"
                                }
                              />
                              <span className="font-mono text-xs text-[var(--text-secondary)]">
                                {b.commitSha?.slice(0, 7) ?? b.imageTag ?? b.id.slice(0, 7)}
                              </span>
                              {b.branch && (
                                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                                  on {b.branch}
                                </span>
                              )}
                            </div>
                          </div>
                          <span className="font-mono text-[10px] text-[var(--text-tertiary)] whitespace-nowrap">
                            {relativeTime(b.startedAt)}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="logs" className="mt-4">
              <Card>
                <CardHeader>
                  <CardTitle>Live logs</CardTitle>
                </CardHeader>
                <CardContent>
                  <LogStream project={project} service={service} env="production" height="55vh" />
                </CardContent>
              </Card>
            </TabsContent>
          </Tabs>
        </>
      )}
    </div>
  );
}
