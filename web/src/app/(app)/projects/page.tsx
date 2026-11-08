"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Compass, LayoutGrid, Plus, Store } from "lucide-react";
import { useProjects } from "@/features/projects";
import { useGithubRepos } from "@/features/github";
import { useCan, Perms } from "@/features/auth";
import { Button } from "@/components/ui/button";
import { ProjectsList } from "@/components/project/list/ProjectsList";
import { ProjectCardSkeleton } from "@/components/project/list/ProjectCard";
import { GettingStartedCard } from "@/components/project/list/GettingStartedCard";
import { useGettingStartedDismissed } from "@/components/project/list/useListPrefs";

const primaryLink =
  "inline-flex h-9 items-center gap-1.5 rounded-md border border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] px-3.5 text-[13px] font-medium text-[var(--btn-primary-fg)] shadow-[var(--shadow-sm)] outline-none transition-colors hover:bg-[var(--btn-primary-bg-hover)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]";
const secondaryLink =
  "inline-flex h-9 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] px-3.5 text-[13px] font-medium text-[var(--text-primary)] outline-none transition-colors hover:bg-[var(--bg-tertiary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]";

export default function ProjectsPage() {
  const router = useRouter();
  const { data, isPending, isError, error, refetch } = useProjects();
  // Instance editors carry projects:create alongside admins; the server
  // filters /api/projects to each caller's grants.
  const canCreate = useCan("projects:create");
  const isAdmin = useCan(Perms.SettingsAdmin);
  // /api/github/repos is projects:create-gated, so editors get a real
  // "is GitHub installed" signal too.
  const githubRepos = useGithubRepos({ enabled: canCreate });
  const [gettingStartedDismissed, dismissGettingStarted] = useGettingStartedDismissed();

  // First-run redirect into the guided onboarding — once per browser, so
  // "skip to dashboard" sticks. An unknown repos state never redirects.
  // The way back afterwards is the empty state / Getting started card.
  useEffect(() => {
    if (!canCreate || isPending || !githubRepos.isSuccess) return;
    if ((data?.length ?? 0) > 0) return;
    if (githubRepos.data.length > 0) return;
    try {
      if (localStorage.getItem("kuso.welcome.redirected") === "1") return;
      localStorage.setItem("kuso.welcome.redirected", "1");
    } catch {
      // Storage may throw in private-browsing modes; redirect anyway.
    }
    router.replace("/welcome");
  }, [isPending, githubRepos.isSuccess, githubRepos.data, canCreate, data, router]);

  const count = data?.length ?? 0;
  const showGettingStarted = canCreate && !isPending && !isError && count > 0 && count <= 2 && !gettingStartedDismissed;

  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-x-4 gap-y-3">
        <div className="min-w-0">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Projects</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Each project is one product. Connect a repo and kuso builds it on every push.
          </p>
        </div>
        {canCreate && count > 0 && (
          <div className="flex items-center gap-2">
            <Link href="/marketplace" className={secondaryLink} aria-label="Marketplace">
              <Store className="h-4 w-4" aria-hidden />
              <span className="hidden sm:inline">Marketplace</span>
            </Link>
            <Link href="/projects/new" className={primaryLink}>
              <Plus className="h-4 w-4" aria-hidden />
              New project
            </Link>
          </div>
        )}
      </header>

      {showGettingStarted && <GettingStartedCard onDismiss={dismissGettingStarted} />}

      {isPending && (
        <>
          <div className="mb-5 h-9 w-full max-w-md rounded-md bg-[var(--bg-secondary)] motion-safe:animate-pulse" />
          <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true" aria-label="Loading projects">
            {[0, 1, 2, 3, 4, 5].map((i) => (
              <ProjectCardSkeleton key={i} />
            ))}
          </ul>
        </>
      )}

      {isError && (
        <div role="alert" className="rounded-lg border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4">
          <p className="text-sm text-[var(--text-primary)]">Couldn&apos;t load projects</p>
          <p className="mt-1 text-[13px] text-[var(--text-secondary)]">{error?.message ?? "Unknown error"}</p>
          <Button variant="outline" size="sm" className="mt-3" onClick={() => refetch()}>
            Retry
          </Button>
        </div>
      )}

      {!isPending && !isError && count === 0 && (
        <div className="flex flex-col items-center rounded-xl border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-6 py-16 text-center">
          <span className="inline-flex h-12 w-12 items-center justify-center rounded-full bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]">
            <LayoutGrid className="h-5 w-5" aria-hidden />
          </span>
          <h2 className="mt-4 font-heading text-lg font-semibold tracking-tight text-[var(--text-primary)]">
            {canCreate ? "Deploy your first project" : "No projects yet"}
          </h2>
          <p className="mt-1.5 max-w-md text-sm text-[var(--text-secondary)]">
            {canCreate
              ? "Connect a GitHub repo and kuso builds it, deploys it and gives you a live URL. Not sure where to start? The guided tour takes three steps."
              : "You don't have access to any projects yet. Ask an admin to grant you access."}
          </p>
          {canCreate && (
            <>
              <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
                <Link href="/projects/new" className={primaryLink}>
                  <Plus className="h-4 w-4" aria-hidden />
                  Create project
                </Link>
                <Link href="/welcome" className={secondaryLink}>
                  <Compass className="h-4 w-4" aria-hidden />
                  Take the tour
                </Link>
              </div>
              <p className="mt-4 text-[13px] text-[var(--text-secondary)]">
                Or{" "}
                <Link href="/marketplace" className="text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--accent)]">
                  deploy a ready-made app
                </Link>
                {isAdmin && (
                  <>
                    {" "}or{" "}
                    <Link href="/settings/import" className="text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--accent)]">
                      import from Coolify
                    </Link>
                  </>
                )}
                .
              </p>
            </>
          )}
        </div>
      )}

      {!isPending && !isError && data && count > 0 && <ProjectsList projects={data} />}
    </div>
  );
}
