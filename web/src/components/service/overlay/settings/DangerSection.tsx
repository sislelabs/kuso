"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useDeleteService, useService } from "@/features/services";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { deleteEnvironment } from "@/features/projects";
import { friendlyApiError } from "@/features/projects/names";

// DangerSection deletes the service (and only the service) — copy
// and the underlying hook must agree to avoid the user confirming
// "service" and getting a project-wide cascade.
export function DangerSection({
  project,
  service,
  env,
}: {
  project: string;
  service: string;
  // The non-production env being viewed: group name + this service's env
  // CR in it. Absent on production (which can't be deleted on its own).
  env?: { group: string; crName: string };
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
      {env && <DeleteEnvBlock project={project} service={service} env={env} />}
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

// DeleteEnvBlock removes this service's env in one env group (e.g. its
// staging), leaving the service's other environments alone.
function DeleteEnvBlock({
  project,
  service,
  env,
}: {
  project: string;
  service: string;
  env: { group: string; crName: string };
}) {
  const router = useRouter();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const del = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: () => deleteEnvironment(project, env.crName),
    onSuccess: () => {
      toast.success(`${service} removed from ${env.group}`);
      setOpen(false);
      qc.invalidateQueries({ queryKey: ["projects", project] });
      router.replace(`/projects/${encodeURIComponent(project)}`);
    },
    onError: (e) => {
      toast.error(friendlyApiError(e, "Failed to delete environment"));
      setOpen(false);
    },
  });
  return (
    <div className="mb-3 rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4">
      <h4 className="text-sm font-semibold">Delete environment</h4>
      <p className="mt-1 text-xs text-[var(--text-secondary)]">
        Removes <span className="font-mono">{service}</span> from{" "}
        <span className="font-mono">{env.group}</span>. Production and other environments stay.
      </p>
      <Button variant="outline" size="sm" className="mt-3" onClick={() => setOpen(true)}>
        <Trash2 className="h-3.5 w-3.5" /> Delete environment
      </Button>
      <ConfirmDialog
        open={open}
        title={`Delete ${env.crName}?`}
        body={
          <p>
            Its pods, env-scoped secrets, volumes and any addon copies made for this environment
            are deleted with their data. Production is not touched.
          </p>
        }
        confirmLabel="Delete environment"
        typeToConfirm={env.crName}
        pending={del.isPending}
        onConfirm={() => del.mutate()}
        onCancel={() => setOpen(false)}
      />
    </div>
  );
}
