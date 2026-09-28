"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useCreateProject } from "@/features/projects";
import { defaultServiceHost, useInstanceDomain } from "@/lib/default-host";
import { restoreFormDraft } from "@/lib/query-client";
import { toast } from "sonner";
import { Plus, ArrowRight, Globe, Store } from "lucide-react";

// Route segments the app owns. A project with one of these names would
// collide with a static page (/projects/new) or be stripped by the
// pathname-based param extraction in lib/dynamic-params.ts, leaving it
// unreachable. Mirrors reservedRouteNames in
// server-go/internal/projects/projects_ops.go — the server rejects
// these too; checking here just gives an instant, friendlier error.
const RESERVED_NAMES = new Set([
  "new",
  "projects",
  "services",
  "addons",
  "envs",
  "logs",
  "settings",
  "invite",
]);

// NewProjectPage creates an empty project — just a name and optional
// base domain. Repos attach later as services (each service owns its
// own repo). The old combined wizard tried to do everything in one
// step, which conflated "the project" with "this one repo" and made
// multi-repo projects impossible.
export default function NewProjectPage() {
  const router = useRouter();
  const create = useCreateProject();

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [baseDomain, setBaseDomain] = useState("");
  // Previews default to enabled — most users want PR-deploy URLs. We
  // surface the toggle so it's not a hidden default; the previous
  // version hardcoded enabled:true and users got surprise preview
  // envs on their first PR.
  const [previewsEnabled, setPreviewsEnabled] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  // Inline validation for the name field — shown under the input and
  // marked via aria-invalid, instead of a toast that auto-dismisses
  // before the user finds the offending field. Toasts stay reserved
  // for server-side failures.
  const [nameError, setNameError] = useState<string | null>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);

  // Post-login draft restore. If the user was mid-create when their
  // session expired (-> /login -> bounced back here), repopulate the
  // text fields they'd already typed so they don't have to retype
  // everything. Drafts older than 30 min are dropped by restoreFormDraft.
  useEffect(() => {
    const draft = restoreFormDraft();
    if (!draft) return;
    if (draft.name) setName(draft.name);
    if (draft.description) setDescription(draft.description);
    if (draft.baseDomain) setBaseDomain(draft.baseDomain);
  }, []);

  const instanceDomain = useInstanceDomain() || "kuso.example.com";
  // A concrete "web" service reads better than a bracketed placeholder.
  const exampleService = "web";
  const previewProject = name.trim() || "my-product";
  const exampleHost = defaultServiceHost(exampleService, previewProject, baseDomain, instanceDomain);
  const sameNameHost = defaultServiceHost(previewProject, previewProject, baseDomain, instanceDomain);

  const onCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(trimmed)) {
      setNameError(
        trimmed
          ? "Lowercase letters, digits, and dashes only; must start/end with a letter or digit; ≤ 63 chars."
          : "Project name is required."
      );
      nameInputRef.current?.focus();
      return;
    }
    if (RESERVED_NAMES.has(trimmed)) {
      setNameError(`"${trimmed}" is reserved — it collides with an app route. Pick another name.`);
      nameInputRef.current?.focus();
      return;
    }
    setNameError(null);
    setSubmitting(true);
    try {
      await create.mutateAsync({
        name: trimmed,
        description: description.trim() || undefined,
        baseDomain: baseDomain.trim() || undefined,
        previews: { enabled: previewsEnabled, ttlDays: 7 },
      });
      toast.success("Project created");
      router.replace(`/projects/${encodeURIComponent(trimmed)}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to create project");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="mx-auto max-w-xl p-6 lg:p-8">
      <header className="mb-6">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">New project</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          A project is a container for services. Add services from the canvas — each can come
          from its own GitHub repo.
        </p>
      </header>

      {/* Two ways to start: a curated one-click app, or an empty project
          you fill with your own services. The marketplace lives here (in
          the create flow) rather than the global nav. */}
      <Link
        href="/marketplace"
        className="group mb-4 flex items-center gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3 transition hover:border-[var(--accent)] hover:bg-[var(--bg-elevated)]"
      >
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--accent)]">
          <Store className="h-4 w-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-[13px] font-medium text-[var(--text-primary)]">
            Start from a template
          </span>
          <span className="block text-[12px] text-[var(--text-secondary)]">
            Deploy a curated app (Gitea, Metabase, Plausible…) in one click.
          </span>
        </span>
        <ArrowRight className="h-4 w-4 shrink-0 text-[var(--text-tertiary)] transition group-hover:translate-x-0.5 group-hover:text-[var(--text-secondary)]" />
      </Link>

      <div className="mb-4 flex items-center gap-3">
        <div className="h-px flex-1 bg-[var(--border-subtle)]" />
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          or start empty
        </span>
        <div className="h-px flex-1 bg-[var(--border-subtle)]" />
      </div>

      <form
        onSubmit={onCreate}
        className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
      >
        <div className="space-y-4 px-4 py-4">
          <Field label="Name" hint="lowercase, dashes; used as the slug" htmlFor="project-name">
            <Input
              id="project-name"
              ref={nameInputRef}
              name="name"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameError(null);
              }}
              placeholder="my-product"
              aria-invalid={nameError ? true : undefined}
              className="h-8 font-mono text-[13px]"
              autoFocus
            />
            {nameError && (
              <p role="alert" className="mt-1 text-[11px] text-[var(--error)]">
                {nameError}
              </p>
            )}
          </Field>
          <Field label="Description" hint="optional; shown on the projects list" htmlFor="project-description">
            <Input
              id="project-description"
              name="description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What this project does"
              className="h-8 text-[13px]"
              maxLength={120}
            />
          </Field>
          <Field label="Base domain" hint="optional; auto from cluster if blank" htmlFor="project-base-domain">
            <Input
              id="project-base-domain"
              name="baseDomain"
              value={baseDomain}
              onChange={(e) => setBaseDomain(e.target.value)}
              placeholder="my-product.example.com"
              className="h-8 font-mono text-[13px]"
            />
          </Field>
          <Field label="Preview envs" hint="open a PR → kuso spins up a preview URL (TTL 7 days)">
            <label className="flex items-center gap-2 text-[13px]">
              <input
                type="checkbox"
                checked={previewsEnabled}
                onChange={(e) => setPreviewsEnabled(e.target.checked)}
                className="h-3.5 w-3.5"
              />
              <span>
                Enable preview deploys
                {previewsEnabled && (
                  <span className="ml-2 font-mono text-[10px] text-[var(--text-tertiary)]">
                    (7-day TTL)
                  </span>
                )}
              </span>
            </label>
          </Field>
          <Field label="URL preview" hint="where a service named web would be served">
            <div className="flex items-center gap-2 rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1.5 font-mono text-[12px] text-[var(--text-secondary)]">
              <Globe className="h-3 w-3 text-[var(--text-tertiary)]" />
              <span className="truncate">https://{exampleHost}</span>
            </div>
            <p className="mt-1 text-[10px] text-[var(--text-tertiary)]">
              A service named after the project is served at{" "}
              <span className="font-mono">{sameNameHost}</span>.
              {baseDomain.trim() && (
                <>
                  {" "}Point a wildcard DNS record{" "}
                  <span className="font-mono">*.{baseDomain.trim().replace(/^\.+|\.+$/g, "")}</span>{" "}
                  (and the bare domain) at the cluster.
                </>
              )}
            </p>
          </Field>
        </div>
        <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] px-4 py-3">
          <Link
            href="/projects"
            className="font-mono text-[10px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
          >
            ← cancel
          </Link>
          <Button type="submit" size="sm" disabled={submitting}>
            <Plus className="h-3.5 w-3.5" />
            {submitting ? "Creating…" : "Create project"}
          </Button>
        </footer>
      </form>

      <p className="mt-4 font-mono text-[10px] text-[var(--text-tertiary)]">
        next: open the project canvas → right-click → <span className="text-[var(--text-secondary)]">Add service</span>{" "}
        <ArrowRight className="inline h-2.5 w-2.5" /> connect a repo and configure the runtime.
      </p>
    </div>
  );
}

function Field({
  label,
  hint,
  htmlFor,
  children,
}: {
  label: string;
  hint?: string;
  htmlFor?: string;
  children: React.ReactNode;
}) {
  const labelClass = "block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";
  return (
    <div className="grid grid-cols-[140px_1fr] items-start gap-3">
      <div>
        {htmlFor ? (
          <label htmlFor={htmlFor} className={labelClass}>
            {label}
          </label>
        ) : (
          <div className={labelClass}>{label}</div>
        )}
        {hint && <div className="mt-0.5 text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
      </div>
      <div>{children}</div>
    </div>
  );
}
