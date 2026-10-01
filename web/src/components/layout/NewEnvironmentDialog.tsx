"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useProject, useAddons, createEnvGroup, type EnvGroupSummary } from "@/features/projects";
import { friendlyApiError } from "@/features/projects/names";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Plus, Database, Share2, Sparkles, AlertTriangle } from "lucide-react";
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
  // Set when the server created the env but flagged literals that may
  // still reach production. Shown in-dialog until dismissed — a toast
  // would vanish before anyone reads it.
  const [warned, setWarned] = useState<EnvGroupSummary | null>(null);

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
    setWarned(null);
    // Seed defaults on each open. Stateful kinds default to fresh,
    // others to shared. Users can flip any of them inline.
    const def: Record<string, AddonPolicy> = {};
    for (const a of addonShorts) {
      def[a.short] = defaultPolicyForKind(a.kind);
    }
    setPolicy(def);
  }, [open, addonShorts]);

  const create = useMutation({
    mutationFn: () => createEnvGroup(project, { name, addonPolicy: policy }),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "envs"] });
      qc.invalidateQueries({ queryKey: ["projects", project, "env-groups"] });
      if (res?.warnings && res.warnings.length > 0) {
        setWarned(res);
        return;
      }
      toast.success(
        `Environment "${name}" created. Review variables in each service — addon refs were rewritten where you picked "fresh".`,
        { duration: 8000 },
      );
      onCreated?.(name);
      onClose();
    },
    onError: (e) => toast.error(friendlyApiError(e, "Failed to create environment")),
  });

  const submit = () => {
    if (!name.trim()) {
      toast.error("Name required");
      return;
    }
    if (name === "production" || name.startsWith("pr-") || name.startsWith("preview-")) {
      toast.error('Names "production", "pr-*" and "preview-*" are reserved for kuso-managed environments');
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

  if (warned) {
    const finish = () => {
      onCreated?.(warned.name);
      onClose();
    };
    return (
      <Dialog open={open} onOpenChange={(next) => !next && finish()}>
        <DialogContent className="flex max-h-[85vh] flex-col gap-0 p-0 sm:max-w-lg">
          <DialogHeader className="gap-0.5 border-b border-[var(--border-subtle)] px-4 py-3 pr-10">
            <DialogTitle className="flex items-center gap-1.5 font-heading">
              <AlertTriangle className="h-4 w-4 text-[var(--warning)]" aria-hidden />
              &ldquo;{warned.name}&rdquo; created — check these variables
            </DialogTitle>
            <DialogDescription className="text-[11px] text-[var(--text-secondary)]">
              These values still name a host under this project&apos;s domain that kuso couldn&apos;t map
              to a clone. Until you change them, this environment may read from or write to production.
            </DialogDescription>
          </DialogHeader>
          <div className="flex-1 space-y-3 overflow-y-auto p-4">
            <ul className="space-y-1 rounded-md border border-[var(--warning)]/40 bg-[var(--warning-subtle)] p-3 font-mono text-[11px] text-[var(--text-primary)]">
              {(warned.warnings ?? []).map((w) => (
                <li key={w} className="break-all">
                  {w}
                </li>
              ))}
            </ul>
            {warned.rewrittenEnvVars && warned.rewrittenEnvVars.length > 0 && (
              <div className="text-[11px] text-[var(--text-secondary)]">
                <p className="font-medium text-[var(--text-primary)]">Rewritten to point at the clones</p>
                <ul className="mt-1 space-y-0.5 font-mono text-[11px]">
                  {warned.rewrittenEnvVars.map((r) => (
                    <li key={r} className="break-all">
                      {r}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
          <DialogFooter className="m-0 rounded-b-2xl px-4 py-3">
            <Button size="sm" onClick={finish}>
              I&apos;ll review them
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && !create.isPending) onClose();
      }}
      disablePointerDismissal={create.isPending}
    >
      <DialogContent
        className="flex max-h-[85vh] flex-col gap-0 p-0 sm:max-w-lg"
        showCloseButton={!create.isPending}
      >
            <DialogHeader className="gap-0.5 border-b border-[var(--border-subtle)] px-4 py-3 pr-10">
              <DialogTitle className="font-heading">New environment</DialogTitle>
              <DialogDescription className="text-[11px] text-[var(--text-tertiary)]">
                  Mirror every service + (optionally) addon under a new name. Send the URL to a
                  client for review without touching production.
              </DialogDescription>
            </DialogHeader>

            <div className="flex-1 overflow-y-auto">
              <div className="space-y-3 border-b border-[var(--border-subtle)] p-4">
                <Field label="name" hint="becomes part of every cloned service's URL" htmlFor="new-env-name">
                  <Input
                    id="new-env-name"
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

            <DialogFooter className="m-0 items-center justify-between gap-2 rounded-b-2xl px-4 py-3 sm:justify-between">
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
            </DialogFooter>
      </DialogContent>
    </Dialog>
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
  htmlFor,
  children,
}: {
  label: string;
  hint?: string;
  htmlFor: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <label
        htmlFor={htmlFor}
        className="block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
      >
        {label}
      </label>
      {children}
      {hint && <div className="text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
    </div>
  );
}
