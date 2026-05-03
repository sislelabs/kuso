"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useCreateProject } from "@/features/projects";
import { toast } from "sonner";
import { Plus, ArrowRight } from "lucide-react";

// NewProjectPage creates an empty project — just a name and optional
// base domain. Repos attach later as services (each service owns its
// own repo). The old combined wizard tried to do everything in one
// step, which conflated "the project" with "this one repo" and made
// multi-repo projects impossible.
export default function NewProjectPage() {
  const router = useRouter();
  const create = useCreateProject();

  const [name, setName] = useState("");
  const [baseDomain, setBaseDomain] = useState("");
  const [submitting, setSubmitting] = useState(false);

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
        baseDomain: baseDomain.trim() || undefined,
        previews: { enabled: true, ttlDays: 7 },
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

      <form
        onSubmit={onCreate}
        className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
      >
        <div className="space-y-4 px-4 py-4">
          <Field label="Name" hint="lowercase, dashes; used as the slug">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-product"
              className="h-8 font-mono text-[13px]"
              autoFocus
            />
          </Field>
          <Field label="Base domain" hint="optional; auto from cluster if blank">
            <Input
              value={baseDomain}
              onChange={(e) => setBaseDomain(e.target.value)}
              placeholder="my-product.example.com"
              className="h-8 font-mono text-[13px]"
            />
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
