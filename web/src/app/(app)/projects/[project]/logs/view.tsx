"use client";

import { useState } from "react";
import { useProject } from "@/features/projects";
import { LogStream } from "@/components/logs/LogStream";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useRouteParams } from "@/lib/dynamic-params";

export function LogsView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const projectName = params.project ?? "";
  const project = useProject(projectName);
  const services = project.data?.services ?? [];
  const [picked, setPicked] = useState<string | null>(null);

  const target = picked ?? services[0]?.metadata.name ?? null;

  if (project.isPending) {
    return (
      <div className="p-6 lg:p-8">
        <Skeleton className="h-8 w-48" />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8 space-y-4">
      <div>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Logs</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Live tail across every pod in the env. Streams over WebSocket, auto-reconnects on drop.
        </p>
      </div>

      {services.length === 0 ? (
        <p className="text-sm text-[var(--text-tertiary)]">
          No services in this project yet.
        </p>
      ) : (
        <>
          <div className="flex flex-wrap gap-2">
            {services.map((s) => (
              <button
                key={s.metadata.name}
                onClick={() => setPicked(s.metadata.name)}
                className={`rounded-md border px-3 py-1.5 text-xs font-mono transition-colors ${
                  target === s.metadata.name
                    ? "border-[var(--accent)] bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                    : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
                }`}
                type="button"
              >
                {s.metadata.name}
              </button>
            ))}
          </div>

          {target && (
            <Card>
              <CardHeader>
                <CardTitle className="font-mono text-sm">{target}</CardTitle>
              </CardHeader>
              <CardContent>
                <LogStream
                  project={projectName}
                  service={target}
                  env="production"
                  height="60vh"
                />
              </CardContent>
            </Card>
          )}
        </>
      )}
    </div>
  );
}
