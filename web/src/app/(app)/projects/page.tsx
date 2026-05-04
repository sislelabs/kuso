"use client";

import Link from "next/link";
import { useProjects } from "@/features/projects";
import { useCan, Perms } from "@/features/auth";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/shared/EmptyState";
import { LayoutGrid, Plus, ArrowUpRight, GitBranch, Globe, Box, Database } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { useQueries } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import type { KusoEnvironment, KusoService, KusoAddon } from "@/types/projects";

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
        <ProjectsGrid projects={data} />
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

interface DescribeResp {
  project: { metadata: { name: string }; spec?: { baseDomain?: string } };
  services: KusoService[];
  environments: KusoEnvironment[];
  addons?: KusoAddon[];
}

// ProjectsGrid fans out a /api/projects/{name} fetch per card so each
// shows live counts (services up, total services, addons). Listing
// already returns the project specs, but Describe is the only call
// that bundles services + envs in one round-trip — cheaper than
// per-card fetches that paginate them separately.
//
// Use useQueries (not N useQuery hooks) so the rules-of-hooks
// invariant holds when projects come and go.
function ProjectsGrid({
  projects,
}: {
  projects: ReadonlyArray<{ metadata: { name: string; uid?: string; creationTimestamp?: string }; spec: { defaultRepo?: { url?: string }; baseDomain?: string; description?: string } }>;
}) {
  const queries = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["projects", p.metadata.name, "describe-summary"],
      queryFn: () => api<DescribeResp>(`/api/projects/${encodeURIComponent(p.metadata.name)}`),
      staleTime: 15_000,
    })),
  });
  return (
    <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {projects.map((p, i) => {
        const name = p.metadata.name;
        const repo = p.spec.defaultRepo?.url?.replace(/^https?:\/\/(www\.)?/, "");
        // Display domain. Fall back to the auto-derived
        // <project>.<server-domain> when baseDomain is unset, since
        // services land at that hostname by default. We don't know
        // the server domain on the client — pull it off the live
        // location so the URL bar's parent suffix wins.
        const explicitDomain = p.spec.baseDomain;
        const inferredDomain = !explicitDomain && typeof window !== "undefined"
          ? `${name}.${window.location.host}`
          : undefined;
        const domain = explicitDomain ?? inferredDomain;
        const created = p.metadata.creationTimestamp
          ? relativeTime(p.metadata.creationTimestamp)
          : null;
        const summary = queries[i]?.data;
        const services = summary?.services ?? [];
        const envs = summary?.environments ?? [];
        const addons = summary?.addons ?? [];
        // "Live" = a production env that's either:
        //   - status.ready === true (helm release Deployed + pod up), or
        //   - status.replicas.ready > 0 (any pod scheduled, even mid-roll)
        //
        // The earlier shape used a flat status.replicas number which
        // the helm-operator never writes — the actual structure mirrors
        // the canvas: status.replicas = {ready, desired, max}. Without
        // matching that the counter always read 0.
        const liveServices = services.filter((s) => {
          const fqn = s.metadata.name;
          const prod = envs.find(
            (e) => e.spec.service === fqn && e.spec.kind === "production"
          );
          if (!prod) return false;
          const st = prod.status as
            | {
                ready?: boolean;
                phase?: string;
                replicas?: { ready?: number; desired?: number; max?: number };
              }
            | undefined;
          if (st?.ready) return true;
          const r = st?.replicas;
          if ((r?.ready ?? 0) > 0 || (r?.desired ?? 0) > 0) return true;
          return false;
        }).length;
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
                {repo && <Row icon={GitBranch} value={repo} />}
                {domain && <Row icon={Globe} value={domain} />}
              </dl>
              {summary && (
                <div className="mt-3 flex items-center gap-3 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  <span className="inline-flex items-center gap-1">
                    <Box className="h-3 w-3" />
                    <span className={liveServices > 0 ? "text-emerald-400" : ""}>
                      {liveServices}/{services.length}
                    </span>
                    <span>live</span>
                  </span>
                  {addons.length > 0 && (
                    <span className="inline-flex items-center gap-1">
                      <Database className="h-3 w-3" />
                      <span>{addons.length}</span>
                      <span>addon{addons.length === 1 ? "" : "s"}</span>
                    </span>
                  )}
                </div>
              )}
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
  );
}
