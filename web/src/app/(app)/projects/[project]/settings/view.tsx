"use client";

import { useState, useEffect } from "react";
import { useRouter } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Separator } from "@/components/ui/separator";
import { useProject, useUpdateProject, useDeleteProject } from "@/features/projects";
import { toast } from "sonner";
import { Trash2, Save } from "lucide-react";

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
  const [confirmDelete, setConfirmDelete] = useState("");

  useEffect(() => {
    if (project.data?.project?.spec) {
      const s = project.data.project.spec;
      setDescription(s.description ?? "");
      setBaseDomain(s.baseDomain ?? "");
      setPreviewsEnabled(!!s.previews?.enabled);
      setPreviewsTtl(s.previews?.ttlDays ?? 7);
    }
  }, [project.data]);

  if (project.isPending) {
    return (
      <div className="p-6 lg:p-8">
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (project.isError) {
    return (
      <div className="p-6 lg:p-8">
        <p className="text-sm text-red-500">{project.error?.message}</p>
      </div>
    );
  }

  const onSave = async () => {
    try {
      await update.mutateAsync({
        description: description || null,
        baseDomain: baseDomain || null,
        previews: { enabled: previewsEnabled, ttlDays: previewsTtl },
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
    <div className="mx-auto max-w-2xl p-6 lg:p-8 space-y-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">{projectName}</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>General</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="description">Description</Label>
            <Input
              id="description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
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
            <p className="text-xs text-[var(--text-tertiary)]">
              Services in this project default to{" "}
              <span className="font-mono">&lt;service&gt;.{baseDomain || "<base>"}</span>
            </p>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Preview environments</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={previewsEnabled}
              onChange={(e) => setPreviewsEnabled(e.target.checked)}
              className="h-4 w-4 rounded border-[var(--border-default)]"
            />
            Spawn a preview env on every PR
          </label>
          {previewsEnabled && (
            <div className="space-y-1.5">
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
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button onClick={onSave} disabled={update.isPending}>
          <Save className="h-4 w-4" />
          {update.isPending ? "Saving…" : "Save"}
        </Button>
      </div>

      <Separator />

      <Card className="border-red-500/30">
        <CardHeader>
          <CardTitle className="text-red-500">Danger zone</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-[var(--text-secondary)]">
            Deleting this project also deletes all services, environments, addons,
            and pods. This action cannot be undone.
          </p>
          <div className="space-y-1.5">
            <Label htmlFor="confirmDelete">
              Type{" "}
              <span className="font-mono text-[var(--text-primary)]">{projectName}</span>{" "}
              to confirm
            </Label>
            <Input
              id="confirmDelete"
              value={confirmDelete}
              onChange={(e) => setConfirmDelete(e.target.value)}
              className="font-mono"
            />
          </div>
          <Button
            variant="destructive"
            onClick={onDelete}
            disabled={del.isPending || confirmDelete !== projectName}
          >
            <Trash2 className="h-4 w-4" />
            Delete project
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
