"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { Check, Minus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useDeleteService, useService } from "@/features/services";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { deleteAddon, deleteEnvironment, useAddons, useEnvironments, useServices } from "@/features/projects";
import { friendlyApiError } from "@/features/projects/names";
import { serviceShortName } from "@/lib/utils";
import { Section, Code } from "./_primitives";
import { exclusiveAddons, serviceEnvs } from "./deletePlan";

// DangerSection deletes the service (and only the service). The confirm
// dialog lists exactly what the server's DeleteService tears down
// (services_ops.go deleteService): every env with its volumes, secrets,
// domains/certs and preview DB copies, plus the service secret, deploy
// hook and build/run history. Project addons are never touched by it, so
// deleting the ones only this service used is an explicit, opt-in
// follow-up call per addon.
export function DangerSection({
  project,
  service,
  env,
  canWrite,
}: {
  project: string;
  service: string;
  // The non-production env being viewed: group name + this service's env
  // CR in it. Absent on production (which can't be deleted on its own).
  env?: { group: string; crName: string };
  canWrite: boolean;
}) {
  const router = useRouter();
  const qc = useQueryClient();
  const del = useDeleteService(project, service);
  const svc = useService(project, service);
  const [open, setOpen] = useState(false);
  // Data for the dialog is only fetched once it opens.
  const services = useServices(open ? project : "");
  const addons = useAddons(open ? project : "");
  const envs = useEnvironments(project);
  const [alsoDelete, setAlsoDelete] = useState<string[]>([]);
  const [working, setWorking] = useState(false);

  const volumes = svc.data?.spec.volumes ?? [];
  const varCount = (svc.data?.spec.envVars ?? []).length;
  const myEnvs = useMemo(() => serviceEnvs(project, service, envs.data ?? []), [project, service, envs.data]);
  const domains = useMemo(() => {
    const out: string[] = [];
    for (const e of envs.data ?? []) {
      if (!myEnvs.some((m) => m.name === e.metadata.name)) continue;
      for (const h of e.spec.additionalHosts ?? []) out.push(h);
      for (const w of e.spec.wildcardDomains ?? []) out.push(w.host);
    }
    return out;
  }, [envs.data, myEnvs]);
  const onlyMine = useMemo(
    () => exclusiveAddons(project, service, services.data ?? [], addons.data ?? []),
    [project, service, services.data, addons.data],
  );
  const kept = (addons.data ?? []).filter((a) => !alsoDelete.includes(a.metadata.name));
  const otherServices = (services.data ?? []).filter((s) => serviceShortName(project, s.metadata.name) !== service);

  const onDelete = async () => {
    setWorking(true);
    try {
      await del.mutateAsync();
    } catch (e) {
      toastError(e, "Failed to delete the service");
      setWorking(false);
      return;
    }
    const failed: string[] = [];
    for (const fqn of alsoDelete) {
      const short = serviceShortName(project, fqn);
      try {
        await deleteAddon(project, short);
      } catch {
        failed.push(short);
      }
    }
    qc.invalidateQueries({ queryKey: ["projects", project] });
    if (failed.length > 0) {
      toast.error(`Service deleted, but these addons couldn't be: ${failed.join(", ")}. Delete them from the canvas.`);
    } else {
      toast.success(alsoDelete.length > 0 ? `${service} and ${alsoDelete.length} addon(s) deleted` : `${service} deleted`);
    }
    setWorking(false);
    // The overlay's URL is gone; land on the project canvas.
    router.replace(`/projects/${encodeURIComponent(project)}`);
  };

  return (
    <Section
      id="danger"
      scope="none"
      title="Danger zone"
      icon={Trash2}
      tone="danger"
      description="These actions can't be undone."
    >
      {env && <DeleteEnvBlock project={project} service={service} env={env} canWrite={canWrite} />}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="text-[13px] font-medium text-[var(--text-primary)]">Delete service</div>
          <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
            Removes <Code>{service}</Code> with all its environments, volumes and variables. You&apos;ll see the full
            list before confirming.
          </p>
        </div>
        <Button
          variant="destructive"
          size="sm"
          className="shrink-0 self-start sm:self-center"
          disabled={!canWrite}
          title={canWrite ? undefined : "Your role can't delete services"}
          onClick={() => {
            setAlsoDelete([]);
            setOpen(true);
          }}
        >
          <Trash2 className="h-3.5 w-3.5" /> Delete service…
        </Button>
      </div>
      <ConfirmDialog
        open={open}
        title={`Delete ${service}?`}
        body={
          <div className="max-h-[50vh] space-y-4 overflow-y-auto pr-1 text-[13px]">
            <div className="space-y-2">
              <p className="font-medium text-[var(--text-primary)]">Permanently deleted</p>
              <ul className="space-y-2">
                <PlanItem
                  title={`${myEnvs.length || "All"} environment${myEnvs.length === 1 ? "" : "s"}`}
                  detail={myEnvs.length > 0 ? myEnvs.map((e) => e.label).join(", ") : "production and any previews"}
                />
                <PlanItem
                  title={volumes.length === 0 ? "No volumes" : `${volumes.length} volume${volumes.length === 1 ? "" : "s"} and their data`}
                  detail={
                    volumes.length === 0
                      ? undefined
                      : volumes.map((v) => `${v.name} (${v.mountPath}, ${v.sizeGi ?? 1} GiB)`).join(", ")
                  }
                  danger={volumes.length > 0}
                  muted={volumes.length === 0}
                />
                <PlanItem
                  title="Variables and secrets"
                  detail={`${varCount} service variable${varCount === 1 ? "" : "s"}, per-environment overrides, the deploy hook, and the repo access token unless another service shares it`}
                />
                <PlanItem
                  title={domains.length === 0 ? "Generated domains and certificates" : `${domains.length} custom domain${domains.length === 1 ? "" : "s"} and their certificates`}
                  detail={domains.length > 0 ? domains.join(", ") : undefined}
                />
                <PlanItem title="Preview database copies" detail="Copies made for this service's preview environments" />
                <PlanItem title="Build and run history" />
              </ul>
            </div>
            {onlyMine.length > 0 && (
              <div className="space-y-2">
                <p className="font-medium text-[var(--text-primary)]">Also delete addons only this service uses?</p>
                <ul className="space-y-1.5">
                  {onlyMine.map((a) => {
                    const short = serviceShortName(project, a.metadata.name);
                    const checked = alsoDelete.includes(a.metadata.name);
                    return (
                      <li key={a.metadata.name}>
                        <label className="flex cursor-pointer items-start gap-2 rounded-md border border-[var(--border-subtle)] px-2.5 py-2 hover:bg-[var(--bg-tertiary)]/50">
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={(e) =>
                              setAlsoDelete((l) =>
                                e.target.checked ? [...l, a.metadata.name] : l.filter((n) => n !== a.metadata.name),
                              )
                            }
                            className="mt-0.5 h-3.5 w-3.5 accent-[var(--error)]"
                          />
                          <span className="min-w-0">
                            <span className="font-mono text-[var(--text-primary)]">{short}</span>{" "}
                            <span className="text-[var(--text-secondary)]">({a.spec.kind})</span>
                            <span className="block text-xs text-[var(--text-secondary)]">
                              {a.spec.external
                                ? "Disconnects it. The external database isn't touched."
                                : "Removes the addon. Its data volume is kept and can be purged from the addon later."}
                            </span>
                          </span>
                        </label>
                      </li>
                    );
                  })}
                </ul>
                <p className="text-xs text-[var(--text-tertiary)]">
                  Based on service subscriptions and variable references. Crons and runs that borrow this service&apos;s
                  variables aren&apos;t checked.
                </p>
              </div>
            )}
            <div className="space-y-2">
              <p className="font-medium text-[var(--text-primary)]">Kept</p>
              <ul className="space-y-2">
                <PlanItem
                  keep
                  title={kept.length === 0 ? "No project addons" : `Project addons (${kept.length})`}
                  detail={kept.length > 0 ? kept.map((a) => serviceShortName(project, a.metadata.name)).join(", ") : undefined}
                  muted={kept.length === 0}
                />
                <PlanItem
                  keep
                  title={`Other services (${otherServices.length})`}
                  detail={otherServices.length > 0 ? otherServices.map((s) => serviceShortName(project, s.metadata.name)).join(", ") : undefined}
                />
              </ul>
            </div>
          </div>
        }
        confirmLabel={alsoDelete.length > 0 ? `Delete service and ${alsoDelete.length} addon${alsoDelete.length === 1 ? "" : "s"}` : "Delete service"}
        typeToConfirm={service}
        pending={working || del.isPending}
        onConfirm={() => void onDelete()}
        onCancel={() => {
          if (!working) setOpen(false);
        }}
      />
    </Section>
  );
}

function PlanItem({
  title,
  detail,
  keep,
  danger,
  muted,
}: {
  title: string;
  detail?: string;
  keep?: boolean;
  danger?: boolean;
  muted?: boolean;
}) {
  const Icon = keep ? Check : muted ? Minus : Trash2;
  return (
    <li className="flex items-start gap-2">
      <Icon
        aria-hidden
        className={
          keep
            ? "mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--success)]"
            : muted
              ? "mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]"
              : "mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--error)]"
        }
      />
      <span className="min-w-0">
        <span className={danger ? "font-medium text-[var(--error)]" : muted ? "text-[var(--text-tertiary)]" : "text-[var(--text-primary)]"}>
          {title}
        </span>
        {detail && <span className="block break-words text-xs text-[var(--text-secondary)]">{detail}</span>}
      </span>
    </li>
  );
}

// DeleteEnvBlock removes this service's env in one env group (e.g. its
// staging), leaving the service's other environments alone.
function DeleteEnvBlock({
  project,
  service,
  env,
  canWrite,
}: {
  project: string;
  service: string;
  env: { group: string; crName: string };
  canWrite: boolean;
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
    <div className="flex flex-col gap-3 border-b border-[var(--border-subtle)] pb-5 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0">
        <div className="text-[13px] font-medium text-[var(--text-primary)]">
          Delete from <span className="font-mono">{env.group}</span>
        </div>
        <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
          Removes <Code>{service}</Code> from this environment only. Production and other environments stay.
        </p>
      </div>
      <Button
        variant="destructive"
        size="sm"
        className="shrink-0 self-start sm:self-center"
        disabled={!canWrite}
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-3.5 w-3.5" /> Delete environment…
      </Button>
      <ConfirmDialog
        open={open}
        title={`Delete ${env.crName}?`}
        body={
          <p>
            Its pods, environment-scoped variables, volumes and any addon copies made for this environment are deleted
            with their data. Production is not touched.
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
