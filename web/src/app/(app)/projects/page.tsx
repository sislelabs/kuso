"use client";

import Link from "next/link";
import { useProjects } from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
// Button used for retry action below; Link used for primary CTAs.
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { LayoutGrid, Plus, ExternalLink } from "lucide-react";
import { relativeTime } from "@/lib/format";

export default function ProjectsPage() {
  const { data, isPending, isError, error, refetch } = useProjects();

  return (
    <div className="mx-auto max-w-6xl p-6 lg:p-8">
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">
            Projects
          </h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Each project is one product. Connect a repo, kuso builds it on every push.
          </p>
        </div>
        <Link
          href="/projects/new"
          className="inline-flex h-9 items-center gap-1.5 rounded-sm border border-transparent bg-primary px-5 text-sm font-medium text-primary-foreground transition-all hover:scale-[1.02]"
        >
          <Plus className="h-4 w-4" />
          New project
        </Link>
      </div>

      {isPending && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-32 w-full" />
          ))}
        </div>
      )}

      {isError && (
        <Card>
          <CardContent className="p-6">
            <p className="text-sm text-red-500">
              Failed to load projects: {error?.message ?? "unknown error"}
            </p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
              Retry
            </Button>
          </CardContent>
        </Card>
      )}

      {!isPending && !isError && data && data.length === 0 && (
        <EmptyState
          icon={<LayoutGrid className="h-5 w-5" />}
          title="No projects yet"
          description="Connect a GitHub repo and kuso will build, deploy, and give you a live URL."
          action={
            <Link
              href="/projects/new"
              className="inline-flex h-9 items-center gap-1.5 rounded-sm bg-primary px-5 text-sm font-medium text-primary-foreground transition-all hover:scale-[1.02]"
            >
              <Plus className="h-4 w-4" />
              Create your first project
            </Link>
          }
        />
      )}

      {!isPending && !isError && data && data.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((p) => (
            <Link
              key={p.metadata.uid ?? p.metadata.name}
              href={`/projects/${p.metadata.name}`}
              className="block"
            >
              <Card>
                <CardHeader>
                  <CardTitle className="flex items-center justify-between gap-2">
                    <span className="truncate">{p.metadata.name}</span>
                    <ExternalLink className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
                  </CardTitle>
                </CardHeader>
                <CardContent>
                  {p.spec.description && (
                    <p className="mb-3 line-clamp-2 text-sm text-[var(--text-secondary)]">
                      {p.spec.description}
                    </p>
                  )}
                  <dl className="space-y-1.5 text-xs">
                    {p.spec.defaultRepo?.url && (
                      <div className="flex items-center gap-2">
                        <dt className="font-mono text-[var(--text-tertiary)]">repo</dt>
                        <dd className="truncate font-mono text-[var(--text-secondary)]">
                          {p.spec.defaultRepo.url.replace(/^https?:\/\/(www\.)?/, "")}
                        </dd>
                      </div>
                    )}
                    {p.spec.baseDomain && (
                      <div className="flex items-center gap-2">
                        <dt className="font-mono text-[var(--text-tertiary)]">domain</dt>
                        <dd className="truncate font-mono text-[var(--text-secondary)]">
                          {p.spec.baseDomain}
                        </dd>
                      </div>
                    )}
                    {p.metadata.creationTimestamp && (
                      <div className="flex items-center gap-2">
                        <dt className="font-mono text-[var(--text-tertiary)]">created</dt>
                        <dd className="font-mono text-[var(--text-secondary)]">
                          {relativeTime(p.metadata.creationTimestamp)}
                        </dd>
                      </div>
                    )}
                  </dl>
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
