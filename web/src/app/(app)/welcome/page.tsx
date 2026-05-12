"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import {
  useInstallURL,
  useInstallations,
  useInstallationRepos,
  type GithubInstallation,
  type GithubRepo,
} from "@/features/github";
import { useCreateProject } from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { CheckCircle2, Github, ArrowRight, ArrowDown, Rocket } from "lucide-react";
import { cn } from "@/lib/utils";
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
  const installations = useInstallations();
  const installs = installations.data ?? [];
  const hasGitHub = installs.length > 0;
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
            installations={installs}
            isLoading={installations.isPending}
            onContinue={() => setStep(2)}
          />
        )}
        {step === 2 && (
          <Step2PickRepo
            installations={installs}
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
                ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-300"
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
  installations,
  isLoading,
  onContinue,
}: {
  installations: GithubInstallation[];
  isLoading: boolean;
  onContinue: () => void;
}) {
  const installURL = useInstallURL();
  const ready = installations.length > 0;
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
        ) : ready ? (
          <>
            <span className="inline-flex items-center gap-1 rounded-md bg-emerald-500/10 px-2 py-1 font-mono text-[11px] text-emerald-300">
              <CheckCircle2 className="h-3 w-3" />
              {installations.length} installation{installations.length === 1 ? "" : "s"} found
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
            ) : (
              <span className="font-mono text-[11px] text-[var(--text-tertiary)]">
                GitHub App not configured yet —{" "}
                <Link href="/settings/github" className="text-[var(--accent)] hover:underline">
                  configure it
                </Link>{" "}
                first.
              </span>
            )}
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
  installations,
  pickedInstall,
  onPickInstall,
  onPicked,
}: {
  installations: GithubInstallation[];
  pickedInstall: number | null;
  onPickInstall: (id: number | null) => void;
  onPicked: (project: string) => void;
}) {
  const installID = pickedInstall ?? installations[0]?.id ?? null;
  const repos = useInstallationRepos(installID ?? 0);
  const createProject = useCreateProject();
  const [busyRepo, setBusyRepo] = useState<string | null>(null);

  if (installations.length === 0) {
    return (
      <div className="text-sm text-[var(--text-secondary)]">
        No GitHub installations yet. Go back to step 1.
      </div>
    );
  }

  const onPick = async (repo: GithubRepo) => {
    setBusyRepo(repo.fullName);
    try {
      // Project slug: lowercased repo name, dashes only.
      const slug = repo.name
        .toLowerCase()
        .replace(/[^a-z0-9-]/g, "-")
        .replace(/^-+|-+$/g, "")
        .slice(0, 63);
      // GithubRepo doesn't carry the full HTTPS URL; we synthesise it
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
        {repos.isPending && [...Array(5)].map((_, i) => <Skeleton key={i} className="h-8 w-full" />)}
        {repos.data?.length === 0 && (
          <li className="px-3 py-4 text-center text-[12px] text-[var(--text-tertiary)]">
            This installation has access to zero repos. Add some on GitHub.
          </li>
        )}
        {(repos.data ?? []).map((r) => (
          <li key={r.id}>
            <button
              type="button"
              onClick={() => onPick(r)}
              disabled={busyRepo !== null}
              className="flex w-full items-center justify-between rounded px-3 py-2 text-left text-[12px] hover:bg-[var(--bg-tertiary)] disabled:opacity-50"
            >
              <span className="flex items-center gap-2">
                <Github className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
                <span className="font-mono">{r.fullName}</span>
                {r.private && (
                  <span className="rounded bg-[var(--bg-tertiary)] px-1 py-0.5 font-mono text-[9px] uppercase text-[var(--text-tertiary)]">
                    private
                  </span>
                )}
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
  const addServicePath = `${projectPath}/services/new`;
  return (
    <div>
      <div className="flex items-start gap-3">
        <CheckCircle2 className="mt-1 h-5 w-5 text-emerald-400" />
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold tracking-tight">
            Project <span className="font-mono">{project}</span> is live
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
        Tip: every service gets a free <code>*.{`{project}`}.kuso</code> domain. Bring your
        own under <Link href={`${projectPath}/settings`} className="text-[var(--accent)] hover:underline">project settings</Link>.
      </p>
    </div>
  );
}
