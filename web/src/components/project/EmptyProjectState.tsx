"use client";

import Link from "next/link";
import { ArrowRight, Database, Github, LayoutTemplate } from "lucide-react";
import { useCanOnProject, Perms } from "@/features/auth";
import { cn } from "@/lib/utils";

const cardCls =
  "group flex h-full flex-col rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-5 text-left shadow-[var(--shadow-sm)] transition-colors hover:border-[var(--border-strong)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent)] disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:border-[var(--border-subtle)]";

// EmptyProjectState is the first screen of a brand-new project: the three
// ways to put something in it, each one click away.
export function EmptyProjectState({ project, onAddDatabase }: { project: string; onAddDatabase: () => void }) {
  const canServices = useCanOnProject(project, Perms.ServicesWrite);
  const canAddons = useCanOnProject(project, Perms.AddonsWrite);
  const p = encodeURIComponent(project);

  return (
    <div className="mx-auto w-full max-w-4xl px-4 py-10 sm:px-6 sm:py-16">
      <h1 className="font-heading text-2xl font-semibold tracking-tight text-[var(--text-primary)]">
        Add the first thing to {project}
      </h1>
      <p className="mt-2 max-w-2xl text-sm text-[var(--text-secondary)]">
        A project holds your services, databases and scheduled jobs. Start with whichever you have; you can add the
        rest later from the canvas.
      </p>

      {!canServices && !canAddons && (
        <p className="mt-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
          You have read-only access to this project. Ask a project editor to add services.
        </p>
      )}

      <div className="mt-8 grid gap-3 sm:grid-cols-3">
        <Option
          href={canServices ? `/projects/${p}/services/new` : undefined}
          icon={<Github className="h-5 w-5" />}
          title="Deploy from GitHub"
          body="Point kuso at a repo. It detects the runtime, builds the image and deploys on every push."
          cta="Choose a repository"
          primary
        />
        <Option
          href={`/marketplace?project=${p}`}
          icon={<LayoutTemplate className="h-5 w-5" />}
          title="Start from a template"
          body="One-click apps from the marketplace: databases with UIs, analytics, CMSs, monitoring and more."
          cta="Browse templates"
        />
        <Option
          onClick={canAddons ? onAddDatabase : undefined}
          icon={<Database className="h-5 w-5" />}
          title="Add a database"
          body="Managed Postgres, Redis, MongoDB and more, with backups. Services in the project get the connection details."
          cta="Pick a database"
        />
      </div>
    </div>
  );
}

function Option({
  href,
  onClick,
  icon,
  title,
  body,
  cta,
  primary,
}: {
  href?: string;
  onClick?: () => void;
  icon: React.ReactNode;
  title: string;
  body: string;
  cta: string;
  primary?: boolean;
}) {
  const inner = (
    <>
      <span
        className={cn(
          "inline-flex h-10 w-10 items-center justify-center rounded-lg border",
          primary
            ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--accent)]"
            : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
        )}
      >
        {icon}
      </span>
      <span className="mt-4 text-base font-semibold text-[var(--text-primary)]">{title}</span>
      <span className="mt-1.5 flex-1 text-sm leading-relaxed text-[var(--text-secondary)]">{body}</span>
      <span
        className={cn(
          "mt-5 inline-flex items-center gap-1 text-sm font-medium",
          primary ? "text-[var(--accent)]" : "text-[var(--text-primary)]",
        )}
      >
        {cta}
        <ArrowRight className="h-4 w-4 transition-transform motion-safe:group-hover:translate-x-0.5" aria-hidden />
      </span>
    </>
  );
  if (href) {
    return (
      <Link href={href} className={cardCls}>
        {inner}
      </Link>
    );
  }
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      title={onClick ? undefined : "Needs editor access on this project"}
      className={cardCls}
    >
      {inner}
    </button>
  );
}
