"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useDeleteProject } from "@/features/projects";

export function DangerSection({
  project,
  service,
}: {
  project: string;
  service: string;
}) {
  const router = useRouter();
  const del = useDeleteProject();
  const [confirming, setConfirming] = useState(false);
  const [confirmText, setConfirmText] = useState("");

  const onDelete = async () => {
    if (confirmText !== service) {
      toast.error("Type the service name to confirm");
      return;
    }
    try {
      await del.mutateAsync(project);
      toast.success("Project deleted");
      router.replace("/projects");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete");
    }
  };

  return (
    <section id="danger" className="scroll-mt-6">
      <header className="mb-2 flex items-center gap-2">
        <Trash2 className="h-3.5 w-3.5 text-red-400" />
        <h3 className="font-heading text-sm font-semibold tracking-tight text-red-400">
          Danger
        </h3>
      </header>
      <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4">
        <h4 className="text-sm font-semibold">Delete project</h4>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Removes the project, every service, every preview env, and tears down the
          running pods. The git repo is untouched. This cannot be undone.
        </p>
        {!confirming ? (
          <Button
            variant="outline"
            size="sm"
            className="mt-3"
            onClick={() => setConfirming(true)}
          >
            <Trash2 className="h-3.5 w-3.5" /> Delete project
          </Button>
        ) : (
          <div className="mt-3 space-y-2">
            <Label htmlFor="confirm-del" className="text-xs">
              Type <span className="font-mono">{service}</span> to confirm
            </Label>
            <Input
              id="confirm-del"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              className="font-mono text-sm"
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button
                variant="destructive"
                size="sm"
                onClick={onDelete}
                disabled={confirmText !== service || del.isPending}
              >
                {del.isPending ? "Deleting…" : "Confirm delete"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setConfirming(false);
                  setConfirmText("");
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
