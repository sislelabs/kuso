"use client";

import Link from "next/link";
import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useSession } from "@/features/auth";
import { Logo } from "@/components/shared/Logo";
import { ThemeToggle } from "@/components/shared/ThemeToggle";
import { ArrowRight, Github, Zap, Database, Rocket, GitBranch } from "lucide-react";

export default function LandingPage() {
  const router = useRouter();
  const { data, isPending } = useSession();

  useEffect(() => {
    if (!isPending && data) router.replace("/projects");
  }, [data, isPending, router]);

  return (
    <div className="min-h-screen bg-[var(--bg-primary)]">
      <header className="flex items-center justify-between px-6 py-4 lg:px-12">
        <Logo />
        <div className="flex items-center gap-3">
          <ThemeToggle />
          <Link
            href="/login"
            className="inline-flex h-9 items-center gap-1.5 rounded-sm bg-primary px-5 text-sm font-medium text-primary-foreground transition-all hover:scale-[1.02]"
          >
            Sign in
            <ArrowRight className="h-3.5 w-3.5" />
          </Link>
        </div>
      </header>

      <main>
        <section className="mx-auto max-w-4xl px-6 pb-12 pt-16 text-center lg:px-12 lg:pt-24">
          <p className="mb-4 inline-block rounded-full border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-secondary)]">
            self-hosted · agent-native PaaS
          </p>
          <h1 className="text-hero font-heading">
            Connect a repo. <span className="text-[var(--accent)]">Deploy.</span>
          </h1>
          <p className="mx-auto mt-6 max-w-2xl text-base text-[var(--text-secondary)] sm:text-lg">
            kuso runs on your own Kubernetes cluster. Push to <span className="font-mono">main</span> and
            it builds + rolls a new image. Open a PR and you get a preview URL. Add a Postgres with one
            click and <span className="font-mono">DATABASE_URL</span> shows up as an env var in every
            service.
          </p>
          <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
            <Link
              href="/login"
              className="inline-flex h-10 items-center gap-1.5 rounded-sm bg-primary px-6 text-sm font-medium text-primary-foreground transition-all hover:scale-[1.02]"
            >
              Sign in to your instance
              <ArrowRight className="h-3.5 w-3.5" />
            </Link>
            <a
              href="https://github.com/sislelabs/kuso"
              target="_blank"
              rel="noreferrer"
              className="inline-flex h-10 items-center gap-1.5 rounded-sm border border-[var(--border-default)] bg-transparent px-6 text-sm font-medium hover:bg-[var(--bg-tertiary)]"
            >
              <Github className="h-4 w-4" />
              View on GitHub
            </a>
          </div>
        </section>

        <section className="mx-auto max-w-5xl px-6 pb-24 lg:px-12">
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Feature
              Icon={Rocket}
              title="One-screen deploy"
              copy="Pick a repo, kuso detects services + suggests addons, click Deploy. No multi-step wizards."
            />
            <Feature
              Icon={GitBranch}
              title="PR previews"
              copy="Every PR gets its own URL, auto-torn down on merge. TTL keeps stragglers off the cluster."
            />
            <Feature
              Icon={Database}
              title="Shared addons"
              copy="One Postgres for the whole project. Connection env vars wired into every service automatically."
            />
            <Feature
              Icon={Zap}
              title="Sleeps when idle"
              copy="Scale-to-zero on idle, autoscale on traffic. Wake on first request, no cold-start drama."
            />
          </div>
        </section>
      </main>

      <footer className="border-t border-[var(--border-subtle)] px-6 py-6 lg:px-12">
        <div className="mx-auto flex max-w-5xl flex-col items-center justify-between gap-2 text-xs text-[var(--text-tertiary)] sm:flex-row">
          <p>© {new Date().getFullYear()} SisleLabs · AGPL-3.0</p>
          <p className="font-mono">
            <a
              href="https://github.com/sislelabs/kuso"
              target="_blank"
              rel="noreferrer"
              className="underline hover:text-[var(--text-secondary)]"
            >
              github.com/sislelabs/kuso
            </a>
          </p>
        </div>
      </footer>
    </div>
  );
}

function Feature({
  Icon,
  title,
  copy,
}: {
  Icon: React.ComponentType<{ className?: string }>;
  title: string;
  copy: string;
}) {
  return (
    <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-5">
      <div className="mb-3 inline-flex h-9 w-9 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--accent)]">
        <Icon className="h-4 w-4" />
      </div>
      <h3 className="mb-1 font-heading text-base font-semibold tracking-tight">{title}</h3>
      <p className="text-sm text-[var(--text-secondary)]">{copy}</p>
    </div>
  );
}
