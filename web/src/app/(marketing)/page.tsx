"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { useSession } from "@/features/auth";
import { Logo } from "@/components/shared/Logo";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/shared/ThemeToggle";
import NotFound from "@/app/not-found";
import { ArrowRight, Github, Zap, Database, Rocket, GitBranch } from "lucide-react";

function isLandingPath(pathname: string): boolean {
  const path = pathname.replace(/\/+$/, "");
  return path === "" || path === "/index.html";
}

export default function LandingPage() {
  const router = useRouter();
  const { data, isPending } = useSession();
  // Older servers answer unknown URLs with this page (200). Render the
  // 404 there instead of the landing — or, logged in, a silent bounce
  // to /projects that hides the bad link.
  const [unknownPath, setUnknownPath] = useState(false);

  useEffect(() => {
    if (!isLandingPath(window.location.pathname)) setUnknownPath(true);
  }, []);

  useEffect(() => {
    if (!isLandingPath(window.location.pathname)) return;
    if (!isPending && data) router.replace("/projects");
  }, [data, isPending, router]);

  if (unknownPath) return <NotFound />;

  return (
    <div className="min-h-screen bg-[var(--bg-primary)]">
      <header className="flex items-center justify-between px-6 py-4 lg:px-12">
        <Logo />
        <div className="flex items-center gap-3">
          <ThemeToggle />
          <Button size="sm" render={<Link href="/login" />} nativeButton={false}>
            Sign in
          </Button>
        </div>
      </header>

      <main>
        <section className="mx-auto max-w-4xl px-6 pb-12 pt-16 text-center lg:px-12 lg:pt-24">
          <p className="mb-5 inline-block rounded-full border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1 text-xs font-medium text-[var(--text-secondary)]">
            A self-hosted, agent-native PaaS
          </p>
          <h1 className="text-hero">Connect a repo. Deploy.</h1>
          <p className="mx-auto mt-6 max-w-2xl text-base text-[var(--text-secondary)] sm:text-lg">
            kuso runs on your own Kubernetes cluster. Push to <span className="font-mono">main</span> and
            it builds + rolls a new image. Open a PR and you get a preview URL. Add a Postgres with one
            click and <span className="font-mono">DATABASE_URL</span> shows up as an env var in every
            service you subscribe (all, by default).
          </p>
          <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
            <Button size="lg" render={<Link href="/login" />} nativeButton={false}>
              Sign in to your instance
              <ArrowRight aria-hidden />
            </Button>
            <Button
              size="lg"
              variant="secondary"
              render={<a href="https://github.com/sislelabs/kuso" target="_blank" rel="noreferrer" />}
              nativeButton={false}
            >
              <Github aria-hidden />
              View on GitHub
            </Button>
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
              copy="One Postgres for the whole project. Connection env vars wired into every subscribed service (all, by default)."
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
    <div className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-5">
      <div className="mb-3 inline-flex h-9 w-9 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-secondary)]">
        <Icon className="h-4 w-4" />
      </div>
      <h3 className="mb-1 text-base font-semibold tracking-tight">{title}</h3>
      <p className="text-sm text-[var(--text-secondary)]">{copy}</p>
    </div>
  );
}
