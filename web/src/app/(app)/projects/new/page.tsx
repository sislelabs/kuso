"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useCreateProject } from "@/features/projects";
import { api } from "@/lib/api-client";
import { restoreFormDraft } from "@/lib/query-client";
import { toast } from "sonner";
import { Plus, ArrowRight, Globe, Store } from "lucide-react";

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

  // Cluster's default baseDomain — when the user hasn't typed one,
  // the resolved URL preview falls back to this so they see exactly
  // what their service domain will look like ("api.cluster.com" not
  // an abstract "{name}.{cluster}").
  const cfg = useQuery<{ baseDomain?: string }>({
    queryKey: ["instance-config"],
    queryFn: () => api("/api/config"),
    staleTime: 60_000,
  });
  const effectiveBase = (baseDomain.trim() || cfg.data?.baseDomain || "").replace(/^\.+|\.+$/g, "");
  // The preview shows what a *service inside this project* will look
  // like, not the project itself. We use "web" as a concrete example
  // so the user reads "web.my-product.example.com" instead of a
  // bracketed placeholder that looks like magic syntax.
  const exampleService = "web";
  const previewBase = effectiveBase || "your-cluster.example.com";

  const onCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(trimmed)) {
      toast.error("Name: lowercase letters, digits, and dashes; ≤ 63 chars");
      return;
    }
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
          <Field label="Name" hint="lowercase, dashes; used as the slug">
            <Input
              name="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-product"
              className="h-8 font-mono text-[13px]"
              autoFocus
            />
          </Field>
          <Field label="Description" hint="optional; shown on the projects list">
            <Input
              name="description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What this project does"
              className="h-8 text-[13px]"
              maxLength={120}
            />
          </Field>
          <Field label="Base domain" hint="optional; auto from cluster if blank">
            <Input
              name="baseDomain"
              value={baseDomain}
              onChange={(e) => setBaseDomain(e.target.value)}
              placeholder={cfg.data?.baseDomain ?? "my-product.example.com"}
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
          <Field label="URL preview" hint="example service URL (services get their own subdomain)">
            <div className="flex items-center gap-2 rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1.5 font-mono text-[12px] text-[var(--text-secondary)]">
              <Globe className="h-3 w-3 text-[var(--text-tertiary)]" />
              <span className="truncate">
                https://<span className="text-[var(--text-tertiary)]">{exampleService}</span>.{previewBase}
              </span>
            </div>
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
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[140px_1fr] items-start gap-3">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          {label}
        </div>
        {hint && <div className="mt-0.5 text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
      </div>
      <div>{children}</div>
    </div>
  );
}
