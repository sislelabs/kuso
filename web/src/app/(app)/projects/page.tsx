"use client";

import Link from "next/link";
import { useProjects } from "@/features/projects";
import { useCan, Perms } from "@/features/auth";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { LayoutGrid, Plus, ArrowUpRight, GitBranch, Globe } from "lucide-react";
import { relativeTime } from "@/lib/format";

// ProjectsPage is the landing dashboard listing every project. Each
// row is a thin card showing the name, repo, base domain, and a small
// "open" affordance. We deliberately don't pre-fetch deploy status —
// the canvas has the live picture, this page is just navigation.
export default function ProjectsPage() {
  const { data, isPending, isError, error, refetch } = useProjects();
  // project:write gates project creation. Users without it can still
  // see + open projects they belong to (the server already filters
  // /api/projects to their memberships).
  const canCreate = useCan(Perms.ProjectWrite);

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
            className="inline-flex h-8 items-center gap-1.5 rounded-md bg-[var(--accent)] px-3 text-xs font-medium text-[var(--accent-foreground)] transition-colors hover:bg-[var(--accent)]/90"
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
          description="Connect a GitHub repo and kuso will build, deploy, and give you a live URL."
          action={
            <Link
              href="/projects/new"
              className="inline-flex h-8 items-center gap-1.5 rounded-md bg-[var(--accent)] px-3 text-xs font-medium text-[var(--accent-foreground)] hover:bg-[var(--accent)]/90"
            >
              <Plus className="h-3.5 w-3.5" />
              Create your first project
            </Link>
          }
        />
      )}

      {!isPending && !isError && data && data.length > 0 && (
        <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((p) => {
            const name = p.metadata.name;
            const repo = p.spec.defaultRepo?.url?.replace(/^https?:\/\/(www\.)?/, "");
            const domain = p.spec.baseDomain;
            const created = p.metadata.creationTimestamp
              ? relativeTime(p.metadata.creationTimestamp)
              : null;
            return (
              <li key={p.metadata.uid ?? name}>
                <Link
                  href={`/projects/${name}`}
                  className="group block rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40"
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0 flex-1">
                      <h2 className="truncate text-sm font-semibold tracking-tight text-[var(--text-primary)]">
                        {name}
                      </h2>
                      {p.spec.description && (
                        <p className="mt-1 line-clamp-2 text-[12px] text-[var(--text-secondary)]">
                          {p.spec.description}
                        </p>
                      )}
                    </div>
                    <ArrowUpRight className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)] transition-colors group-hover:text-[var(--text-primary)]" />
                  </div>
                  <dl className="mt-3 space-y-1 text-[11px]">
                    {repo && (
                      <Row icon={GitBranch} value={repo} />
                    )}
                    {domain && <Row icon={Globe} value={domain} />}
                  </dl>
                  {created && (
                    <p className="mt-3 border-t border-[var(--border-subtle)] pt-2 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                      created {created}
                    </p>
                  )}
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function Row({
  icon: Icon,
  value,
}: {
  icon: React.ComponentType<{ className?: string }>;
  value: string;
}) {
  return (
    <div className="flex items-center gap-1.5 text-[var(--text-tertiary)]">
      <Icon className="h-3 w-3 shrink-0" />
      <span className="truncate font-mono text-[var(--text-secondary)]">{value}</span>
    </div>
  );
}
