"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useProject, createEnvironment } from "@/features/projects";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { X, Plus, GitBranch } from "lucide-react";
import { toast } from "sonner";

interface Props {
  project: string;
  open: boolean;
  onClose: () => void;
  onCreated?: (envShortName: string) => void;
}

// NewEnvironmentDialog opens from the env switcher's "+ new" row.
// Lets the user create a long-lived branch env (think "staging" or
// "qa") with its own URL. Production envs auto-create with the
// service; preview envs are PR-driven; this is the third case.
export function NewEnvironmentDialog({ project, open, onClose, onCreated }: Props) {
  const proj = useProject(project);
  const services = proj.data?.services ?? [];
  // Default to the first service so the picker isn't empty on first
  // render. Multi-service projects let the user choose; we don't
  // create one env per service in a single click — too magic.
  const [serviceName, setServiceName] = useState<string>("");
  const [name, setName] = useState("");
  const [branch, setBranch] = useState("");
  const qc = useQueryClient();

  useEffect(() => {
    if (open) {
      setName("");
      setBranch("");
      const first = services[0];
      if (first) {
        // Service CR names are "<project>-<short>"; the API takes
        // the short form. Strip the project prefix here so the
        // user-visible name matches the rest of the UI.
        const full = first.metadata.name;
        const prefix = project + "-";
        setServiceName(full.startsWith(prefix) ? full.slice(prefix.length) : full);
      }
    }
  }, [open, project, services]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const create = useMutation({
    mutationFn: () => createEnvironment(project, serviceName, { name, branch }),
    onSuccess: () => {
      toast.success(`Environment "${name}" created`);
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      onCreated?.(name);
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Failed to create environment"),
  });

  const submit = () => {
    if (!serviceName) {
      toast.error("Pick a service first");
      return;
    }
    if (!name.trim() || !branch.trim()) {
      toast.error("Name + branch required");
      return;
    }
    if (name === "production" || name.startsWith("pr-")) {
      toast.error('Names "production" and "pr-*" are reserved');
      return;
    }
    if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(name)) {
      toast.error("Name: lowercase, dashes, ≤32 chars");
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
          transition={{ duration: 0.12 }}
          onClick={onClose}
        >
          <motion.div
            initial={{ scale: 0.96, y: 6 }}
            animate={{ scale: 1, y: 0 }}
            exit={{ scale: 0.96, y: 6 }}
            transition={{ type: "spring", stiffness: 360, damping: 32 }}
            className="w-full max-w-md rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)]"
            onClick={(e) => e.stopPropagation()}
          >
            <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
              <div>
                <h2 className="font-heading text-base font-semibold tracking-tight">
                  New environment
                </h2>
                <p className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">
                  Long-lived branch env with its own URL.
                </p>
              </div>
              <button
                type="button"
                aria-label="Close"
                onClick={onClose}
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </header>

            <div className="space-y-3 border-b border-[var(--border-subtle)] p-4">
              <Field label="service" hint="which service this env runs">
                <select
                  value={serviceName}
                  onChange={(e) => setServiceName(e.target.value)}
                  className="h-8 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px]"
                >
                  {services.length === 0 && <option value="">(no services in project)</option>}
                  {services.map((s) => {
                    const full = s.metadata.name;
                    const short = full.startsWith(project + "-")
                      ? full.slice(project.length + 1)
                      : full;
                    return (
                      <option key={full} value={short}>
                        {short}
                      </option>
                    );
                  })}
                </select>
              </Field>

              <Field label="name" hint="becomes part of the URL">
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="staging"
                  className="h-8 font-mono text-[12px]"
                  spellCheck={false}
                  autoFocus
                  onKeyDown={(e) => {
                    if (e.key === "Enter") submit();
                  }}
                />
              </Field>

              <Field
                label="branch"
                hint="the env redeploys when this branch is updated"
                icon={GitBranch}
              >
                <Input
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  placeholder="staging"
                  className="h-8 font-mono text-[12px]"
                  spellCheck={false}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") submit();
                  }}
                />
              </Field>
            </div>

            <footer className="flex items-center justify-end gap-2 px-4 py-3">
              <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={submit}
                disabled={create.isPending || !serviceName || !name.trim() || !branch.trim()}
              >
                <Plus className="h-3 w-3" />
                {create.isPending ? "Creating…" : "Create environment"}
              </Button>
            </footer>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}

function Field({
  label,
  hint,
  icon: Icon,
  children,
}: {
  label: string;
  hint?: string;
  icon?: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {Icon && <Icon className="h-2.5 w-2.5" />}
        {label}
      </div>
      {children}
      {hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
    </div>
  );
}
