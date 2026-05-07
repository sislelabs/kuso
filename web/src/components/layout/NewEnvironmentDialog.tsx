"use client";

import { useEffect, useMemo, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useProject, useAddons, createEnvGroup } from "@/features/projects";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { X, Plus, Database, Share2, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  open: boolean;
  onClose: () => void;
  onCreated?: (envShortName: string) => void;
}

type AddonPolicy = "fresh" | "shared";

// NewEnvironmentDialog spawns a new project-level environment that
// mirrors every service in the project. Use case: client-review
// links — "I want to send this URL to a client and they can poke it
// without touching production." Each cloned service gets its own
// hostname (<svc>-<env>.project.basedomain) and its own KusoBuild
// lineage; addon data isolation is per-addon-policy.
//
// Model:
//   - Name only. No branch field. Branches are configured per-service
//     after the env exists, in the service settings panel.
//   - Per-addon policy picker:
//       fresh  = new addon pod, fresh PVC, new password (isolated data)
//       shared = cloned services point at production's addon (same data)
//     Default per kind: stateful stores (postgres, mongodb, mysql,
//     clickhouse, meilisearch) → fresh; caches/messaging (redis, nats,
//     memcached) → shared by default since cache contention isn't
//     usually a correctness concern. User overrides any of them.
//
// On success: toast + tip banner ("review env vars in [Variables]").
export function NewEnvironmentDialog({ project, open, onClose, onCreated }: Props) {
  const proj = useProject(project);
  const services = proj.data?.services ?? [];
  const addons = useAddons(project);
  const qc = useQueryClient();

  const [name, setName] = useState("");
  const [policy, setPolicy] = useState<Record<string, AddonPolicy>>({});

  // Default the addon-policy map whenever the addon list loads.
  const addonShorts = useMemo(() => {
    const list = addons.data ?? [];
    const out: { short: string; kind: string }[] = [];
    for (const a of list) {
      const fqn = a.metadata.name;
      const prefix = project + "-";
      const short = fqn.startsWith(prefix) ? fqn.slice(prefix.length) : fqn;
      out.push({ short, kind: a.spec?.kind ?? "" });
    }
    out.sort((a, b) => a.short.localeCompare(b.short));
    return out;
  }, [addons.data, project]);

  useEffect(() => {
    if (!open) return;
    setName("");
    // Seed defaults on each open. Stateful kinds default to fresh,
    // others to shared. Users can flip any of them inline.
    const def: Record<string, AddonPolicy> = {};
    for (const a of addonShorts) {
      def[a.short] = defaultPolicyForKind(a.kind);
    }
    setPolicy(def);
  }, [open, addonShorts]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const create = useMutation({
    mutationFn: () => createEnvGroup(project, { name, addonPolicy: policy }),
    onSuccess: () => {
      toast.success(
        `Environment "${name}" created. Review variables in each service — addon refs were rewritten where you picked "fresh".`,
        { duration: 8000 },
      );
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "env-groups"] });
      onCreated?.(name);
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Failed to create environment"),
  });

  const submit = () => {
    if (!name.trim()) {
      toast.error("Name required");
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
    if (services.length === 0) {
      toast.error("Add a service to the project first");
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
            className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] shadow-[var(--shadow-lg)]"
            onClick={(e) => e.stopPropagation()}
          >
            <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
              <div>
                <h2 className="font-heading text-base font-semibold tracking-tight">
                  New environment
                </h2>
                <p className="mt-0.5 text-[11px] text-[var(--text-tertiary)]">
                  Mirror every service + (optionally) addon under a new name. Send the URL to a
                  client for review without touching production.
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

            <div className="flex-1 overflow-y-auto">
              <div className="space-y-3 border-b border-[var(--border-subtle)] p-4">
                <Field label="name" hint="becomes part of every cloned service's URL">
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

                <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 text-[11px] text-[var(--text-secondary)]">
                  <p className="flex items-center gap-1.5 font-medium text-[var(--text-primary)]">
                    <Sparkles className="h-3 w-3 text-[var(--accent)]" />
                    What gets mirrored
                  </p>
                  <ul className="mt-1.5 space-y-0.5 pl-4 text-[var(--text-secondary)]">
                    <li className="list-disc">
                      <span className="font-medium">{services.length}</span>{" "}
                      service{services.length === 1 ? "" : "s"} cloned with all env vars copied
                    </li>
                    <li className="list-disc">
                      Branch defaults to production&apos;s; change per-service in{" "}
                      <span className="font-mono">Settings</span> after create
                    </li>
                    <li className="list-disc">
                      URLs follow{" "}
                      <span className="font-mono text-[10px]">
                        &lt;service&gt;-{name || "<env>"}.{project}.&lt;basedomain&gt;
                      </span>
                    </li>
                  </ul>
                </div>
              </div>

              {addonShorts.length > 0 && (
                <div className="space-y-3 border-b border-[var(--border-subtle)] p-4">
                  <div>
                    <p className="text-[11px] font-mono uppercase tracking-widest text-[var(--text-tertiary)]">
                      addons — pick fresh or shared
                    </p>
                    <p className="mt-1 text-[11px] text-[var(--text-secondary)]">
                      <strong>Fresh</strong> spins up a new pod with its own data. Cloned services
                      get their env-var refs rewritten to point at it.{" "}
                      <strong>Shared</strong> reuses production&apos;s pod — staging writes
                      affect production data.
                    </p>
                  </div>
                  <ul className="space-y-1.5">
                    {addonShorts.map((a) => (
                      <li
                        key={a.short}
                        className="flex items-center justify-between gap-2 rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1.5"
                      >
                        <div className="flex min-w-0 items-center gap-2">
                          <Database className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
                          <div className="min-w-0">
                            <div className="truncate font-mono text-[12px] text-[var(--text-primary)]">
                              {a.short}
                            </div>
                            <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                              {a.kind}
                            </div>
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5 text-[11px]">
                          <PolicyBtn
                            label="fresh"
                            active={policy[a.short] === "fresh"}
                            onClick={() =>
                              setPolicy((p) => ({ ...p, [a.short]: "fresh" }))
                            }
                          />
                          <PolicyBtn
                            label="shared"
                            active={policy[a.short] === "shared"}
                            onClick={() =>
                              setPolicy((p) => ({ ...p, [a.short]: "shared" }))
                            }
                          />
                        </div>
                      </li>
                    ))}
                  </ul>
                  <p className="flex items-start gap-1.5 text-[10px] text-[var(--text-tertiary)]">
                    <Share2 className="mt-0.5 h-3 w-3 shrink-0" />
                    Tip: caches (redis, nats, memcached) are typically safe to share. Stateful
                    stores (postgres, mongodb, mysql) usually want{" "}
                    <span className="font-medium">fresh</span> so a staging migration doesn&apos;t
                    corrupt production.
                  </p>
                </div>
              )}
            </div>

            <footer className="flex items-center justify-between gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
              <p className="text-[10px] text-[var(--text-tertiary)]">
                {services.length === 0 ? (
                  "Add a service first"
                ) : (
                  <>
                    Will create {services.length} services
                    {addonShorts.filter((a) => policy[a.short] === "fresh").length > 0 && (
                      <>
                        {" "}+ {addonShorts.filter((a) => policy[a.short] === "fresh").length}{" "}
                        fresh addon
                        {addonShorts.filter((a) => policy[a.short] === "fresh").length === 1
                          ? ""
                          : "s"}
                      </>
                    )}
                  </>
                )}
              </p>
              <div className="flex items-center gap-2">
                <Button variant="ghost" size="sm" onClick={onClose} disabled={create.isPending}>
                  Cancel
                </Button>
                <Button
                  size="sm"
                  onClick={submit}
                  disabled={create.isPending || !name.trim() || services.length === 0}
                >
                  <Plus className="h-3 w-3" />
                  {create.isPending ? "Mirroring…" : "Create environment"}
                </Button>
              </div>
            </footer>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}

function PolicyBtn({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded px-2 py-1 transition-colors",
        active
          ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
          : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
      )}
    >
      {label}
    </button>
  );
}

function defaultPolicyForKind(kind: string): AddonPolicy {
  // Stateful stores → fresh by default. Caches / message brokers
  // → shared by default. Users can always override per-addon.
  switch (kind.toLowerCase()) {
    case "postgres":
    case "mysql":
    case "mongodb":
    case "clickhouse":
    case "meilisearch":
    case "elasticsearch":
    case "cockroachdb":
    case "couchdb":
    case "s3":
      return "fresh";
    case "redis":
    case "memcached":
    case "nats":
    case "rabbitmq":
    case "kafka":
    case "mailpit":
      return "shared";
    default:
      return "fresh";
  }
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </div>
      {children}
      {hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
    </div>
  );
}
