"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useDeleteService, useService } from "@/features/services";

// DangerSection deletes the service (and only the service) — copy
// and the underlying hook must agree to avoid the user confirming
// "service" and getting a project-wide cascade.
export function DangerSection({
  project,
  service,
}: {
  project: string;
  service: string;
}) {
  const router = useRouter();
  const del = useDeleteService(project, service);
  const svc = useService(project, service);
  const volumes = svc.data?.spec.volumes ?? [];
  const [open, setOpen] = useState(false);

  const onDelete = async () => {
    try {
      await del.mutateAsync();
      toast.success("Service deleted");
      // Land on the project canvas — the service overlay's URL is
      // gone now and a refetch on the project detail page picks up
      // the smaller service list.
      router.replace(`/projects/${encodeURIComponent(project)}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete");
    }
  };

  return (
    <section id="danger" className="scroll-mt-6">
      <header className="mb-2 flex items-center gap-2">
        <Trash2 className="h-3.5 w-3.5 text-[var(--error)]" />
        <h3 className="font-heading text-sm font-semibold tracking-tight text-[var(--error)]">
          Danger
        </h3>
      </header>
      <div className="rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4">
        <h4 className="text-sm font-semibold">Delete service</h4>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Removes <span className="font-mono">{service}</span> and all its environments. Addons
          and other services stay.
        </p>
        <Button variant="outline" size="sm" className="mt-3" onClick={() => setOpen(true)}>
          <Trash2 className="h-3.5 w-3.5" /> Delete service
        </Button>
      </div>
      <ConfirmDialog
        open={open}
        title={`Delete ${service}?`}
        body={
          <div className="space-y-2">
            <p>Removes the service and every environment (production + previews).</p>
            {volumes.length > 0 && (
              <div>
                <p className="text-[var(--error)]">These volumes and their data are deleted:</p>
                <ul className="mt-1 space-y-0.5 font-mono text-[11px]">
                  {volumes.map((v) => (
                    <li key={v.name}>
                      {v.name} <span className="text-[var(--text-tertiary)]">{v.mountPath}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        }
        confirmLabel="Delete service"
        typeToConfirm={service}
        pending={del.isPending}
        onConfirm={() => void onDelete()}
        onCancel={() => setOpen(false)}
      />
    </section>
  );
}
