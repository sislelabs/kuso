"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { addAddon } from "@/features/projects";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { X, Plus } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  open: boolean;
  onClose: () => void;
}

// Kinds the operator's helm chart actually knows how to render. The
// "unsupported" template emits a no-op for anything else, so the user
// would get a CR but no actual workload — better to gate the picker.
const KINDS = [
  "postgres",
  "redis",
  "mongodb",
  "mysql",
  "rabbitmq",
  "memcached",
  "clickhouse",
  "elasticsearch",
  "kafka",
  "cockroachdb",
  "couchdb",
] as const;

// AddAddonDialog opens from the canvas right-click menu and lets the
// operator drop a new addon (postgres / redis / etc) into the
// project. Two-step: pick a kind from the grid, then name it. The
// name auto-fills with the kind so single-of-each-type stays one
// click. We don't surface size / version / HA toggles yet — defaults
// from the chart cover ~all indie use cases; advanced lives in
// kuso.yml.
export function AddAddonDialog({ project, open, onClose }: Props) {
  const [kind, setKind] = useState<string>("");
  const [name, setName] = useState<string>("");
  const qc = useQueryClient();

  // Reset state every open so leftover input from a cancelled run
  // doesn't leak into the next attempt.
  useEffect(() => {
    if (open) {
      setKind("");
      setName("");
    }
  }, [open]);

  // ESC closes — same affordance as the rest of the overlays.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const create = useMutation({
    mutationFn: () => addAddon(project, { name, kind }),
    onSuccess: () => {
      toast.success(`${addonLabel(kind)} addon "${name}" created`);
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Failed to create addon"),
  });

  const onSubmit = () => {
    if (!kind) {
      toast.error("Pick an addon type first");
      return;
    }
    if (!name.trim()) {
      toast.error("Give the addon a name");
      return;
    }
    if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(name.trim())) {
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
            className="w-full max-w-lg rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)]"
            onClick={(e) => e.stopPropagation()}
          >
            <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
              <div>
                <h2 className="font-heading text-base font-semibold tracking-tight">Add addon</h2>
                <p className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">
                  Connection envs are wired into every service in{" "}
                  <span className="font-mono text-[var(--text-secondary)]">{project}</span>.
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

            {/* Kind picker grid */}
            <div className="grid grid-cols-3 gap-2 border-b border-[var(--border-subtle)] p-3">
              {KINDS.map((k) => {
                const active = kind === k;
                return (
                  <button
                    key={k}
                    type="button"
                    onClick={() => {
                      setKind(k);
                      // Auto-fill the name with the kind for the
                      // common one-of-each case. Don't overwrite if
                      // the user already typed something.
                      if (!name.trim()) setName(k);
                    }}
                    className={cn(
                      "flex h-16 flex-col items-start gap-1 rounded-md border px-3 py-2 text-left transition-colors",
                      active
                        ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)]"
                        : "border-[var(--border-subtle)] bg-[var(--bg-secondary)] hover:bg-[var(--bg-tertiary)]/50"
                    )}
                  >
                    <AddonIcon kind={k} />
                    <span className="text-[12px] font-medium">{addonLabel(k)}</span>
                  </button>
                );
              })}
            </div>

            {/* Name field */}
            <div className="space-y-2 border-b border-[var(--border-subtle)] p-3">
              <label
                htmlFor="addon-name"
                className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
              >
                name
              </label>
              <Input
                id="addon-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={kind || "db"}
                onKeyDown={(e) => {
                  if (e.key === "Enter") onSubmit();
                }}
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
                autoFocus={!!kind}
              />
              <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                Becomes the helm release name. Lowercase, dashes, ≤32 chars.
              </p>
            </div>

            <footer className="flex items-center justify-end gap-2 px-4 py-3">
              <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={onSubmit}
                disabled={create.isPending || !kind || !name.trim()}
              >
                <Plus className="h-3 w-3" />
                {create.isPending ? "Creating…" : "Add addon"}
              </Button>
            </footer>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
