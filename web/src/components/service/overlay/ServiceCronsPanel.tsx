"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { listServiceCrons, addCron, deleteCron, syncCron } from "@/features/services";
import type { KusoCron } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import { Clock, Plus, Trash2, RefreshCw, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
}

// ServiceCronsPanel — list + create + delete + sync crons attached to
// this service. The CRD's `service` field is the FQN
// (`<project>-<short>`) but the user only sees the cron's short name.
// The server resolves the parent's image + envFromSecrets at create
// time so every cron run has the same connection envs as the live
// service pod. "Sync" re-resolves after a redeploy so the cron picks
// up the new image.
export function ServiceCronsPanel({ project, service }: Props) {
  const qc = useQueryClient();
  const canWrite = useCan(Perms.ServicesWrite);
  const [adding, setAdding] = useState(false);

  const list = useQuery<KusoCron[]>({
    queryKey: ["projects", project, "services", service, "crons"],
    queryFn: () => listServiceCrons(project, service),
    refetchInterval: 30_000,
  });

  return (
    <div className="space-y-3 p-5">
      <header className="flex items-center justify-between">
        <div>
          <h3 className="font-mono text-sm font-medium">Crons</h3>
          <p className="font-mono text-[11px] text-[var(--text-tertiary)]">
            Recurring jobs that run the parent service&apos;s image with a custom command.
          </p>
        </div>
        {canWrite && !adding && (
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            <Plus className="h-3.5 w-3.5" /> Add cron
          </Button>
        )}
      </header>

      {adding && (
        <CronCreateForm
          project={project}
          service={service}
          onClose={() => setAdding(false)}
          onCreated={() => {
            setAdding(false);
            qc.invalidateQueries({
              queryKey: ["projects", project, "services", service, "crons"],
            });
          }}
        />
      )}

      {list.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : list.isError ? (
        <p className="font-mono text-[11px] text-red-400">
          Failed to load: {list.error instanceof Error ? list.error.message : "unknown error"}
        </p>
      ) : (list.data ?? []).length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-4 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
          No crons yet. Click <span className="font-mono">Add cron</span> to schedule one.
        </p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)]">
          {(list.data ?? []).map((c) => (
            <CronRow key={c.metadata.name} project={project} service={service} cron={c} canWrite={canWrite} />
          ))}
        </ul>
      )}
    </div>
  );
}

function CronRow({
  project,
  service,
  cron,
  canWrite,
}: {
  project: string;
  service: string;
  cron: KusoCron;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  // The CR name is "<project>-<service-short>-<cron-short>"; the
  // user sees the trailing short. We strip the "<project>-<svc>-"
  // prefix for display + use the FQN for the API path.
  const fqn = cron.metadata.name;
  const short = displayShort(project, service, fqn);
  const [confirming, setConfirming] = useState(false);

  const del = useMutation({
    mutationFn: () => deleteCron(project, service, fqn),
    onSuccess: () => {
      toast.success(`Cron ${short} deleted`);
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "crons"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });

  const sync = useMutation({
    mutationFn: () => syncCron(project, service, fqn),
    onSuccess: () => {
      toast.success(`Cron ${short} synced`);
      qc.invalidateQueries({ queryKey: ["projects", project, "services", service, "crons"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Sync failed"),
  });

  return (
    <li className="flex items-center gap-3 px-3 py-2">
      <Clock className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
      <div className="min-w-0 flex-1">
        <p className="truncate font-mono text-[12px] font-medium">{short}</p>
        <p className="truncate font-mono text-[10px] text-[var(--text-tertiary)]">
          <span className="text-[var(--text-secondary)]">{cron.spec.schedule}</span>
          {" · "}
          {cron.spec.command.join(" ")}
        </p>
      </div>
      {cron.spec.suspend && (
        <span className="rounded bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px] text-amber-400">
          suspended
        </span>
      )}
      {canWrite && (
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={() => sync.mutate()}
            disabled={sync.isPending}
            title="Re-resolve image + env from production"
            className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-40"
          >
            <RefreshCw className={cn("h-3.5 w-3.5", sync.isPending && "animate-spin")} />
          </button>
          {!confirming ? (
            <button
              type="button"
              onClick={() => setConfirming(true)}
              className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400"
              title="Delete cron"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </button>
          ) : (
            <div className="inline-flex items-center gap-1">
              <Button
                size="sm"
                variant="ghost"
                disabled={del.isPending}
                onClick={() => del.mutate()}
                className="h-6 px-2 text-[10px] text-red-400"
              >
                {del.isPending ? "…" : "yes"}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setConfirming(false)}
                disabled={del.isPending}
                className="h-6 px-2 text-[10px]"
              >
                no
              </Button>
            </div>
          )}
        </div>
      )}
    </li>
  );
}

function CronCreateForm({
  project,
  service,
  onClose,
  onCreated,
}: {
  project: string;
  service: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [schedule, setSchedule] = useState("0 3 * * *");
  const [cmd, setCmd] = useState("");
  const create = useMutation({
    mutationFn: () =>
      addCron(project, service, {
        name: name.trim(),
        schedule: schedule.trim(),
        // Split on whitespace; users that need shell quoting use
        // `sh -c "<cmd>"`. Same convention as the CLI.
        command: cmd.trim().split(/\s+/).filter(Boolean),
      }),
    onSuccess: () => {
      toast.success(`Cron ${name} created`);
      setName("");
      setCmd("");
      onCreated();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Create failed"),
  });
  const submitDisabled = !name.trim() || !schedule.trim() || !cmd.trim() || create.isPending;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!submitDisabled) create.mutate();
      }}
      className="space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3"
    >
      <div className="flex items-center justify-between">
        <p className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          New cron
        </p>
        <button
          type="button"
          onClick={onClose}
          className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
        >
          <X className="h-3 w-3" />
        </button>
      </div>
      <div className="grid grid-cols-[1fr_180px] gap-2">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="cron name (e.g. nightly-cleanup)"
          className="h-7 font-mono text-[12px]"
        />
        <Input
          value={schedule}
          onChange={(e) => setSchedule(e.target.value)}
          placeholder="*/15 * * * *"
          className="h-7 font-mono text-[12px]"
        />
      </div>
      <Input
        value={cmd}
        onChange={(e) => setCmd(e.target.value)}
        placeholder="rails runner Cleanup.run    OR    sh -c &quot;curl … &amp;&amp; …&quot;"
        className="h-7 font-mono text-[12px]"
      />
      <div className="flex items-center justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onClose} type="button" disabled={create.isPending}>
          Cancel
        </Button>
        <Button size="sm" type="submit" disabled={submitDisabled}>
          {create.isPending ? "Creating…" : "Create cron"}
        </Button>
      </div>
    </form>
  );
}

// displayShort strips the "<project>-<service-short>-" prefix off the
// CR name so the user sees just the short cron name they typed.
function displayShort(project: string, serviceFQN: string, cronFQN: string): string {
  // serviceFQN is already "<project>-<short>"; the cron CR adds another "-<short>".
  const svcShort = serviceFQN.startsWith(project + "-") ? serviceFQN.slice(project.length + 1) : serviceFQN;
  const prefix = `${project}-${svcShort}-`;
  return cronFQN.startsWith(prefix) ? cronFQN.slice(prefix.length) : cronFQN;
}
