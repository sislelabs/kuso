"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { useInstallURL, useGithubRepos, type GithubRepoRef } from "@/features/github";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useCreateProject } from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { CheckCircle2, Github, ArrowRight, ArrowDown, Rocket } from "lucide-react";
import { cn } from "@/lib/utils";
import { defaultServiceHost, useInstanceDomain } from "@/lib/default-host";
import { toast } from "sonner";

// Guided 3-step onboarding for users landing on a fresh kuso install.
// Before this, the path from "I just signed up" to "I have a service
// running" was four screens of empty states with no guidance — users
// either bounced or learned by trial and error.
//
// Step 1: install the GitHub App. We surface the install URL and a
// status-poller; once the user comes back from GitHub.com with at
// least one installation visible, we advance.
//
// Step 2: pick a repo. Inline list of repos under the chosen
// installation; clicking creates a project named after the repo and
// jumps to Step 3.
//
// Step 3: confirm. Lands on the new project's canvas with a banner
// pointing at "Add service" — the project exists, the user knows
// where the next click is.
//
// The page reads ?step= from the URL so it's bookmarkable + the
// back button works.

type Step = 1 | 2 | 3;

export default function WelcomePage() {
  const router = useRouter();
  const search = useSearchParams();
  const stepParam = search?.get("step");
  const step = ((stepParam ? parseInt(stepParam, 10) : 1) || 1) as Step;
  const project = search?.get("project") ?? "";
  // /api/github/repos (projects:create) rather than the admin-only
  // installations endpoints, so instance editors can onboard too.
  const repos = useGithubRepos();
  const repoList = repos.data ?? [];
  const installCount = new Set(repoList.map((r) => r.installationId)).size;
  const hasGitHub = installCount > 0;
  const [pickedInstall, setPickedInstall] = useState<number | null>(null);

  const setStep = (n: Step, params: Record<string, string> = {}) => {
    const usp = new URLSearchParams(search?.toString() ?? "");
    usp.set("step", String(n));
    for (const [k, v] of Object.entries(params)) {
      if (v) usp.set(k, v);
    }
    router.replace(`/welcome?${usp.toString()}`, { scroll: false });
  };

  return (
    <div className="mx-auto max-w-2xl p-6 lg:p-8">
      <header className="mb-8">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Welcome to kuso</h1>
        <p className="mt-2 text-sm text-[var(--text-secondary)]">
          Three steps from here to a running service. You can leave the wizard at any time —{" "}
          <Link href="/projects" className="text-[var(--accent)] hover:underline">
            skip to the dashboard
          </Link>
          .
        </p>
      </header>

      <Stepper current={step} hasGitHub={hasGitHub} />

      <div className="mt-8 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-6">
        {step === 1 && (
          <Step1InstallGitHub
            installCount={installCount}
            isLoading={repos.isPending}
            isChecking={repos.isFetching}
            error={repos.isError ? repos.error : null}
            onCheckAgain={() => void repos.refetch()}
            onContinue={() => setStep(2)}
          />
        )}
        {step === 2 && (
          <Step2PickRepo
            repos={repoList}
            isLoading={repos.isPending}
            error={repos.isError ? repos.error : null}
            onRetry={() => void repos.refetch()}
            pickedInstall={pickedInstall}
            onPickInstall={setPickedInstall}
            onPicked={(p) => setStep(3, { project: p })}
          />
        )}
        {step === 3 && <Step3Deploy project={project} />}
      </div>

      <p className="mt-4 text-center text-[10px] text-[var(--text-tertiary)]">
        Already migrating from Coolify? <Link href="/settings/import" className="text-[var(--accent)] hover:underline">Import from there</Link> instead.
      </p>
    </div>
  );
}

function Stepper({ current, hasGitHub }: { current: Step; hasGitHub: boolean }) {
  const steps = [
    { id: 1 as Step, label: "Install GitHub App", done: hasGitHub },
    { id: 2 as Step, label: "Pick a repo", done: current > 2 },
    { id: 3 as Step, label: "Deploy", done: false },
  ];
  return (
    <ol className="flex items-center gap-2">
      {steps.map((s, i) => (
        <li key={s.id} className="flex items-center gap-2">
          <span
            className={cn(
              "inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-[10px] font-mono",
              s.done
                ? "border-emerald-500/40 bg-emerald-500/10 text-[var(--success)]"
                : s.id === current
                  ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--accent)]"
                  : "border-[var(--border-subtle)] text-[var(--text-tertiary)]"
            )}
          >
            {s.done ? <CheckCircle2 className="h-3 w-3" /> : s.id}
          </span>
          <span
            className={cn(
              "font-mono text-[11px]",
              s.id === current ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]"
            )}
          >
            {s.label}
          </span>
          {i < steps.length - 1 && (
            <ArrowRight className="h-3 w-3 mx-1 text-[var(--text-tertiary)]" />
          )}
        </li>
      ))}
    </ol>
  );
}

function Step1InstallGitHub({
  installCount,
  isLoading,
  isChecking,
  error,
  onCheckAgain,
  onContinue,
}: {
  installCount: number;
  isLoading: boolean;
  isChecking: boolean;
  error: unknown;
  onCheckAgain: () => void;
  onContinue: () => void;
}) {
  const installURL = useInstallURL();
  const ready = installCount > 0;
  return (
    <div>
      <div className="flex items-start gap-3">
        <Github className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold tracking-tight">Install the kuso GitHub App</h2>
          <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
            kuso needs read access to your repos to clone them at build time and
            write commit statuses back. The app is installed on a per-org basis
            — pick the orgs/repos you want kuso to see; you can change this
            later on GitHub.
          </p>
        </div>
      </div>
      <div className="mt-5 flex flex-wrap items-center gap-3">
        {isLoading ? (
          <Skeleton className="h-8 w-40" />
        ) : error ? (
          <QueryErrorState what="GitHub repos" error={error} onRetry={onCheckAgain} className="w-full" />
        ) : ready ? (
          <>
            <span className="inline-flex items-center gap-1 rounded-md bg-emerald-500/10 px-2 py-1 font-mono text-[11px] text-[var(--success)]">
              <CheckCircle2 className="h-3 w-3" />
              {installCount} installation{installCount === 1 ? "" : "s"} found
            </span>
            <Button onClick={onContinue} size="sm">
              Continue
              <ArrowRight className="h-3 w-3" />
            </Button>
          </>
        ) : (
          <>
            {installURL.data?.url ? (
              <a
                href={installURL.data.url}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex h-8 items-center gap-1.5 rounded-md bg-[var(--accent)] px-3 text-xs font-medium text-[var(--accent-foreground)] hover:bg-[var(--accent)]/90"
              >
                <Github className="h-3.5 w-3.5" />
                Install on GitHub
              </a>
            ) : installURL.isError ? (
              // Non-admins 403 on /api/github/install-url, and the
              // /settings/github page is admin-only too. The previous
              // copy said "ask an admin" with no path forward — UX
              // P0-A from the pass-4 review. Now we offer a real
              // alternative: deploy a pre-built image (no GitHub App
              // needed; the runtime=image path landed alongside this
              // change). Plus a mailto fallback so a user on a fresh
              // install can ping the admin without leaving the page.
              <div className="space-y-2">
                <p className="font-mono text-[11px] text-[var(--text-tertiary)]">
                  GitHub App requires an admin. Two paths forward:
                </p>
                <div className="flex flex-wrap items-center gap-2">
                  <Link
                    href="/projects/new"
                    className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2 font-mono text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]/80 hover:text-[var(--text-primary)]"
                  >
                    Deploy a pre-built image →
                  </Link>
                  <a
                    href="mailto:?subject=kuso GitHub App install request&body=Hi%20%E2%80%94%20I%27d%20like%20to%20deploy%20a%20service%20on%20our%20kuso%20instance%20but%20the%20GitHub%20App%20isn%27t%20installed%20yet.%20Could%20you%20configure%20it%20at%20%2Fsettings%2Fgithub%3F%20Thanks!"
                    className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-transparent px-2 font-mono text-[11px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                  >
                    Email an admin
                  </a>
                </div>
              </div>
            ) : (
              <span className="font-mono text-[11px] text-[var(--text-tertiary)]">
                GitHub App not configured yet —{" "}
                <Link href="/settings/github" className="text-[var(--accent)] hover:underline">
                  configure it
                </Link>{" "}
                first.
              </span>
            )}
            <Button onClick={onCheckAgain} variant="outline" size="sm" disabled={isChecking}>
              {isChecking ? "Checking…" : "Check again"}
            </Button>
            <Button onClick={onContinue} variant="outline" size="sm">
              Skip — I&apos;ll do this later
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

function Step2PickRepo({
  repos,
  isLoading,
  error,
  onRetry,
  pickedInstall,
  onPickInstall,
  onPicked,
}: {
  repos: GithubRepoRef[];
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  pickedInstall: number | null;
  onPickInstall: (id: number | null) => void;
  onPicked: (project: string) => void;
}) {
  // One entry per installation, labelled by the owner of its first repo.
  const installations = Array.from(
    new Map(repos.map((r) => [r.installationId, r.fullName.split("/")[0]])),
    ([id, accountLogin]) => ({ id, accountLogin }),
  );
  const installID = pickedInstall ?? installations[0]?.id ?? null;
  const installRepos = repos.filter((r) => r.installationId === installID);
  const createProject = useCreateProject();
  const [busyRepo, setBusyRepo] = useState<string | null>(null);

  if (isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }
  if (error) {
    return <QueryErrorState what="GitHub repos" error={error} onRetry={onRetry} />;
  }
  if (installations.length === 0) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-[var(--text-secondary)]">
          No GitHub installations yet. You can install the kuso GitHub
          App first, or start a project without a repo and connect one
          later from its service settings.
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <Link
            href="/welcome?step=1"
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] px-3 font-mono text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
          >
            <ArrowRight className="h-3 w-3 rotate-180" />
            Back to Step 1
          </Link>
          <Link
            href="/projects/new"
            className="inline-flex h-8 items-center gap-1.5 rounded-md bg-[var(--accent)] px-3 text-xs font-medium text-[var(--accent-foreground)] hover:bg-[var(--accent)]/90"
          >
            Start without a repo
            <ArrowRight className="h-3 w-3" />
          </Link>
        </div>
      </div>
    );
  }

  const onPick = async (repo: GithubRepoRef) => {
    setBusyRepo(repo.fullName);
    try {
      // Project slug: lowercased repo name, dashes only.
      const slug = (repo.fullName.split("/")[1] ?? repo.fullName)
        .toLowerCase()
        .replace(/[^a-z0-9-]/g, "-")
        .replace(/^-+|-+$/g, "")
        .slice(0, 63);
      // GithubRepoRef doesn't carry the full HTTPS URL; we synthesise it
      // from fullName since GitHub installations always live on
      // github.com (kuso doesn't yet support GHE Server).
      const repoURL = `https://github.com/${repo.fullName}`;
      await createProject.mutateAsync({
        name: slug,
        description: `Imported from ${repo.fullName}`,
        defaultRepo: {
          url: repoURL,
          defaultBranch: repo.defaultBranch,
        },
        github: installID ? { installationId: installID } : undefined,
        previews: { enabled: true, ttlDays: 7 },
      });
      onPicked(slug);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to create project");
    } finally {
      setBusyRepo(null);
    }
  };

  return (
    <div>
      <h2 className="text-sm font-semibold tracking-tight">Pick a repo to start with</h2>
      <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
        We&apos;ll create a project named after the repo. You can add more
        services + repos to it from the canvas.
      </p>

      {installations.length > 1 && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Org
          </span>
          {installations.map((inst) => (
            <button
              key={inst.id}
              type="button"
              onClick={() => onPickInstall(inst.id)}
              className={cn(
                "inline-flex h-7 items-center gap-1.5 rounded-md border px-2 font-mono text-[11px]",
                installID === inst.id
                  ? "border-[var(--accent)]/50 bg-[var(--accent-subtle)] text-[var(--accent)]"
                  : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
              )}
            >
              {inst.accountLogin}
            </button>
          ))}
        </div>
      )}

      <ul className="mt-4 max-h-80 space-y-1 overflow-y-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-1">
        {installRepos.map((r) => (
          <li key={r.fullName}>
            <button
              type="button"
              onClick={() => onPick(r)}
              disabled={busyRepo !== null}
              className="flex w-full items-center justify-between rounded px-3 py-2 text-left text-[12px] hover:bg-[var(--bg-tertiary)] disabled:opacity-50"
            >
              <span className="flex items-center gap-2">
                <Github className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
                <span className="font-mono">{r.fullName}</span>
              </span>
              {busyRepo === r.fullName ? (
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">creating…</span>
              ) : (
                <ArrowDown className="h-3 w-3 -rotate-90 text-[var(--text-tertiary)]" />
              )}
            </button>
          </li>
        ))}
      </ul>

      <p className="mt-3 font-mono text-[10px] text-[var(--text-tertiary)]">
        or{" "}
        <Link href="/projects/new" className="text-[var(--accent)] hover:underline">
          start a project without a repo
        </Link>{" "}
        — connect repos to its services later.
      </p>
    </div>
  );
}

// Step3Deploy is the "you made it" landing screen. The project exists
// at this point (Step 2 already POSTed /api/projects); what's missing
// is the first service inside it. The previous version of /welcome
// just routed straight to the project canvas, which surfaced the
// "no services" empty state with no breadcrumb back to onboarding.
// Users would land there, stare at the empty grid, and bounce.
//
// Now: Step 3 anchors the wizard's final state. A bright "deploy your
// first service" CTA links into the service-create wizard for the
// new project, with a secondary link to open the project canvas as-is
// if the user wants to look around first. The route preserves
// ?step=3&project=<slug> so the back button returns here, not to the
// repo picker.
function Step3Deploy({ project }: { project: string }) {
  const instanceDomain = useInstanceDomain();
  if (!project) {
    // Defensive: if someone deep-links /welcome?step=3 with no
    // project we degrade to "go to dashboard" rather than rendering
    // a broken CTA that 404s.
    return (
      <div className="text-sm text-[var(--text-secondary)]">
        Project wasn&apos;t recorded. Head to the{" "}
        <Link href="/projects" className="text-[var(--accent)] hover:underline">
          dashboard
        </Link>{" "}
        to find what you created.
      </div>
    );
  }
  const projectPath = `/projects/${encodeURIComponent(project)}`;
  const exampleHost = defaultServiceHost("web", project, "", instanceDomain || "<kuso-domain>");
  const addServicePath = `${projectPath}/services/new`;
  return (
    <div>
      <div className="flex items-start gap-3">
        <CheckCircle2 className="mt-1 h-5 w-5 text-[var(--success)]" />
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold tracking-tight">
            Project <span className="font-mono">{project}</span> created
          </h2>
          <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
            One more click. Pick the part of the repo to deploy first
            — kuso will detect the runtime (Dockerfile / nixpacks /
            buildpacks / static) and start a build immediately.
          </p>
        </div>
      </div>

      <div className="mt-5 rounded-md border border-[var(--accent)]/30 bg-[var(--accent-subtle)] p-4">
        <div className="flex items-start gap-3">
          <Rocket className="mt-0.5 h-4 w-4 text-[var(--accent)]" />
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium text-[var(--text-primary)]">
              Deploy your first service
            </div>
            <p className="mt-1 text-[11px] text-[var(--text-secondary)]">
              The first build typically finishes in 60–90s. While it
              runs, you can configure env vars, attach a Postgres
              addon, or wire a custom domain.
            </p>
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <Link
                href={addServicePath}
                className="inline-flex h-8 items-center gap-1.5 rounded-md bg-[var(--accent)] px-3 text-xs font-medium text-[var(--accent-foreground)] hover:bg-[var(--accent)]/90"
              >
                Add service
                <ArrowRight className="h-3 w-3" />
              </Link>
              <Link
                href={projectPath}
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border-subtle)] px-3 font-mono text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
              >
                Open project canvas
              </Link>
            </div>
          </div>
        </div>
      </div>

      <p className="mt-4 font-mono text-[10px] text-[var(--text-tertiary)]">
        Tip: each service gets its own URL, e.g. a service named web is served at{" "}
        <code>{exampleHost}</code>. Bring your own domain under <Link href={`${projectPath}/settings`} className="text-[var(--accent)] hover:underline">project settings</Link>.
      </p>
    </div>
  );
}
