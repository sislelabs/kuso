"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { X, Globe, Terminal, Box, Clock, Save, Trash2 } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { CronPicker } from "@/components/shared/CronPicker";

// EditCronDialog opens when a CronNode is clicked. Lets the user edit
// schedule / target / suspend state, and delete the cron entirely.
//
// What's editable depends on kind:
//   http    — URL, schedule, suspend
//   command — image, command, schedule, suspend
//   service — schedule, suspend, command (the parent service supplies
//             image + envFromSecrets via SyncFromService)
//
// The CR name is immutable (rename = clone-then-delete); we don't
// expose it here. Display name is editable in a separate row so users
// can change the canvas label without touching anything load-bearing.

interface CronShape {
  metadata: { name: string };
  spec: {
    project?: string;
    kind?: string;
    service?: string;
    url?: string;
    schedule?: string;
    command?: string[];
    suspend?: boolean;
    displayName?: string;
  };
}

interface Props {
  project: string;
  cron: CronShape | null;
  onClose: () => void;
}

const KIND_ICONS: Record<string, LucideIcon> = {
  http: Globe,
  command: Terminal,
  service: Box,
};

function shortName(project: string, fqn: string): string {
  // Project-scoped cron: "<project>-<short>"
  // Service-attached cron: "<project>-<svc>-<short>"
  // The DELETE path is /api/projects/{p}/crons/{tail} for project
  // crons; the per-service variant uses a different route entirely.
  // We always pass the FULL tail (everything after "<project>-") to
  // the project-scope DELETE since that's what server's
  // DeleteProject expects.
  const prefix = project + "-";
  return fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
}

export function EditCronDialog({ project, cron, onClose }: Props) {
  // Local state mirrors the form. Re-baselined when the parent swaps
  // cron prop (different node clicked) or refetch lands fresh data.
  const [displayName, setDisplayName] = useState("");
  const [schedule, setSchedule] = useState("0 3 * * *");
  const [url, setUrl] = useState("");
  const [imageRepo, setImageRepo] = useState("");
  const [imageTag, setImageTag] = useState("latest");
  const [cmd, setCmd] = useState("");
  const [suspend, setSuspend] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    if (!cron) return;
    setDisplayName(cron.spec.displayName ?? "");
    setSchedule(cron.spec.schedule ?? "0 3 * * *");
    setUrl(cron.spec.url ?? "");
    setSuspend(!!cron.spec.suspend);
    setCmd((cron.spec.command ?? []).join(" "));
  }, [cron]);

  const qc = useQueryClient();

  const isProjectScoped = cron
    ? !cron.spec.service || (cron.spec.kind ?? "service") !== "service"
    : false;
  const tail = cron ? shortName(project, cron.metadata.name) : "";

  const save = useMutation({
    mutationFn: async () => {
      if (!cron) throw new Error("no cron");
      // Two surfaces:
      //   project-scoped (kind=http / kind=command): PATCH /api/
      //     projects/{p}/crons/{name} — preserves CR identity so
      //     helm-operator does an in-place update (no gap where
      //     the cronjob is missing).
      //   service-attached (kind=service): PATCH /api/.../services/
      //     {svc}/crons/{name} — the existing endpoint that knows
      //     how to re-resolve image + envFromSecrets from the
      //     parent service.
      if (isProjectScoped) {
        const kind = (cron.spec.kind ?? "service").toLowerCase();
        const body: Record<string, unknown> = {
          displayName: displayName.trim(),
          schedule: schedule.trim(),
          suspend,
        };
        if (kind === "http") {
          body.url = url.trim();
        } else if (kind === "command") {
          if (imageRepo.trim()) {
            body.image = {
              repository: imageRepo.trim(),
              ...(imageTag.trim() ? { tag: imageTag.trim() } : {}),
            };
          }
          body.command = cmd.trim().split(/\s+/).filter(Boolean);
        }
        return api(
          `/api/projects/${encodeURIComponent(project)}/crons/${encodeURIComponent(tail)}`,
          { method: "PATCH", body },
        );
      }
      // Service-attached cron: PATCH the existing endpoint.
      const svc = cron.spec.service ?? "";
      const cronShort = shortName(project + "-" + svc.replace(project + "-", ""), cron.metadata.name);
      return api(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(svc)}/crons/${encodeURIComponent(cronShort)}`,
        {
          method: "PATCH",
          body: {
            schedule: schedule.trim(),
            suspend,
            command: cmd.trim().split(/\s+/).filter(Boolean),
          },
        },
      );
    },
    onSuccess: () => {
      toast.success("Cron saved");
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "crons"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  const del = useMutation({
    mutationFn: async () => {
      if (!cron) throw new Error("no cron");
      if (isProjectScoped) {
        return api(
          `/api/projects/${encodeURIComponent(project)}/crons/${encodeURIComponent(tail)}`,
          { method: "DELETE" },
        );
      }
      const svc = cron.spec.service ?? "";
      const cronShort = shortName(project + "-" + svc.replace(project + "-", ""), cron.metadata.name);
      return api(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(svc)}/crons/${encodeURIComponent(cronShort)}`,
        { method: "DELETE" },
      );
    },
    onSuccess: () => {
      toast.success("Cron deleted");
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "crons"] });
      setConfirmDelete(false);
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });

  if (!cron) return null;
  const kind = (cron.spec.kind ?? "service").toLowerCase();
  const Icon = KIND_ICONS[kind] ?? Clock;
  const cronTail = tail || cron.metadata.name;

  return (
    <AnimatePresence>
      {cron && (
        <motion.div
          className="fixed inset-0 z-[55] flex items-center justify-center bg-[rgba(8,8,11,0.6)] p-4"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          onClick={onClose}
        >
          <motion.div
            initial={{ scale: 0.96, y: 4 }}
            animate={{ scale: 1, y: 0 }}
            exit={{ scale: 0.96, y: 4 }}
            transition={{ duration: 0.12 }}
            onClick={(e) => e.stopPropagation()}
            className="flex max-h-[92vh] w-full max-w-lg flex-col overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)]"
          >
            <header className="flex items-start justify-between gap-3 border-b border-[var(--border-subtle)] px-4 py-3">
              <div className="flex min-w-0 items-start gap-2">
                <Icon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
                <div className="min-w-0">
                  <h2 className="truncate font-heading text-base font-semibold tracking-tight">
                    {displayName.trim() || cronTail}
                  </h2>
                  <p className="mt-0.5 truncate font-mono text-[11px] text-[var(--text-tertiary)]">
                    {project} · {cronTail} · {kind}
                  </p>
                </div>
              </div>
              <button
                type="button"
                onClick={onClose}
                aria-label="Close"
                className="rounded-md p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-4 w-4" />
              </button>
            </header>

            <div className="flex-1 space-y-4 overflow-y-auto px-4 py-4">
              <div>
                <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  label
                </label>
                <Input
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  placeholder={cronTail}
                  className="mt-1 h-7 text-[12px]"
                  spellCheck={false}
                />
              </div>

              <div>
                <label className="block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  schedule
                </label>
                <div className="mt-1.5">
                  <CronPicker value={schedule} onChange={setSchedule} />
                </div>
              </div>

              {kind === "http" && (
                <div>
                  <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    URL
                  </label>
                  <Input
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    placeholder="https://api.example.com/cron/cleanup"
                    className="mt-1 h-7 font-mono text-[11px]"
                    spellCheck={false}
                  />
                </div>
              )}

              {kind === "command" && (
                <>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_120px]">
                    <div>
                      <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                        image
                      </label>
                      <Input
                        value={imageRepo}
                        onChange={(e) => setImageRepo(e.target.value)}
                        placeholder="ghcr.io/owner/repo"
                        className="mt-1 h-7 font-mono text-[11px]"
                        spellCheck={false}
                      />
                    </div>
                    <div>
                      <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                        tag
                      </label>
                      <Input
                        value={imageTag}
                        onChange={(e) => setImageTag(e.target.value)}
                        placeholder="latest"
                        className="mt-1 h-7 font-mono text-[11px]"
                        spellCheck={false}
                      />
                    </div>
                  </div>
                  <div>
                    <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                      command
                    </label>
                    <Input
                      value={cmd}
                      onChange={(e) => setCmd(e.target.value)}
                      placeholder='sh -c "rake cleanup"'
                      className="mt-1 h-7 font-mono text-[11px]"
                      spellCheck={false}
                    />
                  </div>
                </>
              )}

              {kind === "service" && (
                <div>
                  <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    command
                  </label>
                  <Input
                    value={cmd}
                    onChange={(e) => setCmd(e.target.value)}
                    placeholder="rails runner Cleanup.run"
                    className="mt-1 h-7 font-mono text-[11px]"
                    spellCheck={false}
                  />
                  <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                    Image + envFromSecrets come from {cron.spec.service}.
                  </p>
                </div>
              )}

              <label className={cn(
                "flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-[12px]",
                suspend
                  ? "border-amber-500/40 bg-amber-500/5"
                  : "border-[var(--border-subtle)] bg-[var(--bg-secondary)]",
              )}>
                <input
                  type="checkbox"
                  checked={suspend}
                  onChange={(e) => setSuspend(e.target.checked)}
                  className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
                />
                <span>
                  Pause this cron — schedule stays saved but no Jobs run until
                  unpaused.
                </span>
              </label>
            </div>

            <footer className="flex items-center justify-between gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
              <Button
                variant="ghost"
                size="sm"
                type="button"
                onClick={() => setConfirmDelete(true)}
                disabled={save.isPending || del.isPending}
                className="text-red-400 hover:text-red-300"
              >
                <Trash2 className="h-3.5 w-3.5" />
                Delete
              </Button>
              <div className="flex items-center gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={onClose}
                  disabled={save.isPending || del.isPending}
                >
                  Cancel
                </Button>
                <Button
                  size="sm"
                  onClick={() => save.mutate()}
                  disabled={save.isPending || del.isPending}
                >
                  <Save className="h-3 w-3" />
                  {save.isPending ? "Saving…" : "Save"}
                </Button>
              </div>
            </footer>
          </motion.div>

          <ConfirmDialog
            open={confirmDelete}
            title="Delete this cron?"
            destructive
            confirmLabel={del.isPending ? "Deleting…" : "Delete"}
            pending={del.isPending}
            body={
              <p className="text-[12px] text-[var(--text-secondary)]">
                Removes the KusoCron and its kube CronJob.{" "}
                {kind === "http" ? "Probe" : kind === "command" ? "Job" : "Cron"} runs stop
                immediately.
              </p>
            }
            onConfirm={() => del.mutate()}
            onCancel={() => {
              if (!del.isPending) setConfirmDelete(false);
            }}
          />
        </motion.div>
      )}
    </AnimatePresence>
  );
}
