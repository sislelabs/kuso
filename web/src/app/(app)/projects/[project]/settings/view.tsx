"use client";

import { useState, useEffect } from "react";
import { useRouter } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useProject, useUpdateProject, useDeleteProject } from "@/features/projects";
import { SharedSecretsCard } from "@/components/project/SharedSecretsCard";
import { toast } from "sonner";
import { Trash2, Save, Settings as SettingsIcon, AlertTriangle } from "lucide-react";

// Project settings — flat layout, sections separated by horizontal
// rules + small uppercase headers. Mirrors the polish of /settings
// instead of stacking Card components which created visual noise.
export function ProjectSettingsView() {
  const params = useRouteParams<{ project: string }>(["project"]);
  const router = useRouter();
  const projectName = params.project ?? "";
  const project = useProject(projectName);
  const update = useUpdateProject(projectName);
  const del = useDeleteProject();

  const [description, setDescription] = useState("");
  const [baseDomain, setBaseDomain] = useState("");
  const [previewsEnabled, setPreviewsEnabled] = useState(false);
  const [previewsTtl, setPreviewsTtl] = useState<number>(7);
  const [alwaysOn, setAlwaysOn] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState("");

  useEffect(() => {
    if (project.data?.project?.spec) {
      const s = project.data.project.spec;
      setDescription(s.description ?? "");
      setBaseDomain(s.baseDomain ?? "");
      setPreviewsEnabled(!!s.previews?.enabled);
      setPreviewsTtl(s.previews?.ttlDays ?? 7);
      setAlwaysOn(!!s.alwaysOn);
    }
  }, [project.data]);

  if (project.isPending) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (project.isError) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-400">
          {project.error?.message}
        </p>
      </div>
    );
  }

  const onSave = async () => {
    try {
      await update.mutateAsync({
        description: description || null,
        baseDomain: baseDomain || null,
        previews: { enabled: previewsEnabled, ttlDays: previewsTtl },
        alwaysOn,
      });
      toast.success("Saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    }
  };

  const onDelete = async () => {
    if (confirmDelete !== projectName) {
      toast.error("Type the project name to confirm");
      return;
    }
    try {
      await del.mutateAsync(projectName);
      toast.success("Project deleted");
      router.replace("/projects");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete");
    }
  };

  return (
    <div className="mx-auto max-w-3xl space-y-10 p-6 lg:p-8">
      <header className="flex items-start gap-3">
        <SettingsIcon className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Project settings</h1>
          <p className="mt-1 font-mono text-[12px] text-[var(--text-secondary)]">{projectName}</p>
        </div>
      </header>

      {/* General */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            general
          </h2>
        </header>
        <div className="space-y-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          <div className="space-y-1.5">
            <Label htmlFor="description">Description</Label>
            <Input
              id="description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Short human-readable summary"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="baseDomain">Base domain</Label>
            <Input
              id="baseDomain"
              value={baseDomain}
              onChange={(e) => setBaseDomain(e.target.value)}
              placeholder="myproject.example.com"
              className="font-mono"
            />
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              Services in this project default to{" "}
              <code className="rounded bg-[var(--bg-tertiary)] px-1">
                &lt;service&gt;.{baseDomain || "<base>"}
              </code>
            </p>
          </div>
        </div>
      </section>

      {/* Preview environments */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            preview environments
          </h2>
        </header>
        <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={previewsEnabled}
              onChange={(e) => setPreviewsEnabled(e.target.checked)}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span className="flex-1">
              <span className="text-[13px] font-medium">Spawn a preview env on every PR</span>
              <span className="mt-0.5 block text-[11px] text-[var(--text-tertiary)]">
                Requires a GitHub App install + the project repo set under Cluster config →
                GitHub. Per-PR DB clones are off by default —{" "}
                <code className="font-mono">KUSO_PREVIEW_DB_ENABLED=true</code> on the server to opt in.
              </span>
            </span>
          </label>
          {previewsEnabled && (
            <div className="space-y-1.5 pl-6">
              <Label htmlFor="previewsTtl">Auto-expire after (days)</Label>
              <Input
                id="previewsTtl"
                type="number"
                value={previewsTtl}
                min={1}
                max={30}
                onChange={(e) => setPreviewsTtl(parseInt(e.target.value, 10) || 7)}
                className="w-32 font-mono"
              />
            </div>
          )}
        </div>
      </section>

      {/* Scaling */}
      <section className="space-y-4">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            scaling
          </h2>
        </header>
        <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-4">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={alwaysOn}
              onChange={(e) => setAlwaysOn(e.target.checked)}
              className="mt-0.5 h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span className="flex-1">
              <span className="text-[13px] font-medium">Always-on services (disable scale-to-zero)</span>
              <span className="mt-0.5 block text-[11px] text-[var(--text-tertiary)]">
                Overrides every service&apos;s individual{" "}
                <code className="font-mono">spec.sleep</code> setting. With this on, services in
                this project never scale below their <code className="font-mono">scale.min</code>{" "}
                replica count regardless of idle time. Useful for low-traffic but cold-start-
                sensitive workloads.
              </span>
            </span>
          </label>
        </div>
      </section>

      {/* Save */}
      <div className="flex justify-end">
        <Button onClick={onSave} disabled={update.isPending}>
          <Save className="h-4 w-4" />
          {update.isPending ? "Saving…" : "Save changes"}
        </Button>
      </div>

      {/* Project secrets — flat now, no Card wrapper */}
      <SharedSecretsCard project={projectName} />

      {/* Danger zone */}
      <section className="space-y-3">
        <header className="flex items-center gap-2">
          <AlertTriangle className="h-4 w-4 text-red-400" />
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-red-400">
            danger zone
          </h2>
        </header>
        <div className="space-y-3 rounded-md border border-red-500/30 bg-red-500/5 p-4">
          <p className="text-[12px] leading-relaxed text-[var(--text-secondary)]">
            Deleting this project also deletes all services, environments, addons, and pods. This
            action cannot be undone.
          </p>
          <div className="space-y-1.5">
            <Label htmlFor="confirmDelete" className="text-[12px]">
              Type{" "}
              <span className="font-mono text-[var(--text-primary)]">{projectName}</span> to
              confirm
            </Label>
            <Input
              id="confirmDelete"
              value={confirmDelete}
              onChange={(e) => setConfirmDelete(e.target.value)}
              className="font-mono"
              spellCheck={false}
              autoComplete="off"
            />
          </div>
          <Button
            variant="destructive"
            size="sm"
            onClick={onDelete}
            disabled={del.isPending || confirmDelete !== projectName}
          >
            <Trash2 className="h-4 w-4" />
            Delete project
          </Button>
        </div>
      </section>
    </div>
  );
}
