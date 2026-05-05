"use client";

import { useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { X, Plus, Globe, Terminal } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { CronPicker } from "@/components/shared/CronPicker";

interface Props {
  project: string;
  open: boolean;
  onClose: () => void;
}

// AddCronDialog opens from the canvas right-click menu. Two flavours:
//
//   http    → runs `curl <url>` on schedule. Useful for hitting a
//             /healthz endpoint, pinging a sibling service, calling
//             out to a webhook. The kuso-backup image already has
//             curl + ca-certificates baked in so no user image is
//             needed.
//
//   command → runs a user-supplied image with a user-supplied argv.
//             Useful for one-off scripts that don't belong to any
//             specific service ("delete users older than 90 days").
//
// Service-attached crons (the legacy kind) still live on the per-
// service Crons tab — those reuse a sibling service's image and
// envFromSecrets, so the right surface is the service overlay.
type Kind = "http" | "command";

export function AddCronDialog({ project, open, onClose }: Props) {
  const [kind, setKind] = useState<Kind>("http");
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [schedule, setSchedule] = useState("0 3 * * *");
  const [url, setUrl] = useState("");
  const [imageRepo, setImageRepo] = useState("");
  const [imageTag, setImageTag] = useState("latest");
  const [cmd, setCmd] = useState("");

  const qc = useQueryClient();

  const create = useMutation({
    mutationFn: async () => {
      const body: {
        name: string;
        displayName?: string;
        kind: Kind;
        schedule: string;
        url?: string;
        image?: { repository: string; tag?: string };
        command?: string[];
      } = {
        name: name.trim(),
        displayName: displayName.trim() || undefined,
        kind,
        schedule: schedule.trim(),
      };
      if (kind === "http") {
        body.url = url.trim();
      } else {
        body.image = {
          repository: imageRepo.trim(),
          ...(imageTag.trim() ? { tag: imageTag.trim() } : {}),
        };
        body.command = cmd
          .trim()
          .split(/\s+/)
          .filter(Boolean);
      }
      return api(`/api/projects/${encodeURIComponent(project)}/crons`, {
        method: "POST",
        body,
      });
    },
    onSuccess: () => {
      toast.success(`Cron "${name}" added`);
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "crons"] });
      onClose();
      // Reset for the next open.
      setName("");
      setDisplayName("");
      setUrl("");
      setImageRepo("");
      setImageTag("latest");
      setCmd("");
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Failed to add cron"),
  });

  const onSubmit = () => {
    if (!name.trim()) {
      toast.error("Give the cron a name");
      return;
    }
    if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(name.trim())) {
      toast.error("Name: lowercase, dashes, ≤32 chars");
      return;
    }
    if (!schedule.trim()) {
      toast.error("Pick a schedule");
      return;
    }
    if (kind === "http" && !url.trim()) {
      toast.error("HTTP crons need a URL");
      return;
    }
    if (kind === "command" && !imageRepo.trim()) {
      toast.error("Command crons need an image");
      return;
    }
    if (kind === "command" && !cmd.trim()) {
      toast.error("Command crons need a command");
      return;
    }
    create.mutate();
  };

  return (
    <AnimatePresence>
      {open && (
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
            className="w-full max-w-lg overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)]"
          >
            <header className="flex items-start justify-between gap-3 border-b border-[var(--border-subtle)] px-4 py-3">
              <div>
                <h2 className="font-heading text-base font-semibold tracking-tight">
                  Add cron
                </h2>
                <p className="mt-0.5 text-[11px] text-[var(--text-secondary)]">
                  A recurring job in <span className="font-mono">{project}</span>.
                </p>
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

            <div className="space-y-4 px-4 py-4">
              {/* Kind picker */}
              <div className="grid grid-cols-2 gap-2">
                <KindTile
                  selected={kind === "http"}
                  onClick={() => setKind("http")}
                  icon={Globe}
                  label="HTTP probe"
                  hint="curl a URL on schedule"
                />
                <KindTile
                  selected={kind === "command"}
                  onClick={() => setKind("command")}
                  icon={Terminal}
                  label="Command"
                  hint="run an image + argv"
                />
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    name
                  </label>
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="nightly-cleanup"
                    className="mt-1 h-7 font-mono text-[11px]"
                    spellCheck={false}
                  />
                </div>
                <div>
                  <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    label (optional)
                  </label>
                  <Input
                    value={displayName}
                    onChange={(e) => setDisplayName(e.target.value)}
                    placeholder="Nightly Cleanup"
                    className="mt-1 h-7 text-[11px]"
                    spellCheck={false}
                  />
                </div>
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
                  <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                    Cron fails on non-2xx. Add auth via the URL (token query
                    string) or hit a sibling service via{" "}
                    <code>${"{{ svc.URL }}/cron/..."}</code> at deploy time.
                  </p>
                </div>
              )}

              {kind === "command" && (
                <>
                  <div className="grid grid-cols-[1fr_120px] gap-2">
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
                    <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                      Whitespace-split into argv. First element is the binary.
                    </p>
                  </div>
                </>
              )}
            </div>

            <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
              <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>
                Cancel
              </Button>
              <Button size="sm" onClick={onSubmit} disabled={create.isPending}>
                <Plus className="h-3 w-3" />
                {create.isPending ? "Adding…" : "Add cron"}
              </Button>
            </footer>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}

function KindTile({
  selected,
  onClick,
  icon: Icon,
  label,
  hint,
}: {
  selected: boolean;
  onClick: () => void;
  icon: typeof Globe;
  label: string;
  hint: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex flex-col items-start gap-1 rounded-md border px-3 py-2 text-left transition-colors",
        selected
          ? "border-[var(--accent)]/60 bg-[var(--accent)]/10"
          : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] hover:border-[var(--border-default)]",
      )}
    >
      <Icon className={cn("h-4 w-4", selected ? "text-[var(--accent)]" : "text-[var(--text-tertiary)]")} />
      <span className="text-[12px] font-medium">{label}</span>
      <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{hint}</span>
    </button>
  );
}
