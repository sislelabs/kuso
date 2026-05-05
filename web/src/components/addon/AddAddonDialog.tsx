"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { addAddon } from "@/features/projects";
import { api } from "@/lib/api-client";
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
type Mode = "managed" | "external" | "instance";

export function AddAddonDialog({ project, open, onClose }: Props) {
  const [kind, setKind] = useState<string>("");
  const [name, setName] = useState<string>("");
  const [mode, setMode] = useState<Mode>("managed");
  const [extSecret, setExtSecret] = useState<string>("");
  const [extKeys, setExtKeys] = useState<string>("");
  const [instName, setInstName] = useState<string>("");
  const qc = useQueryClient();

  // Pull the list of admin-registered instance addons so the picker
  // can show a dropdown instead of a free-text input. Hits the
  // /names variant (gated by addons:write rather than settings:admin)
  // so non-admins can still see what's available. Empty list when no
  // shared servers are registered — UI falls back to the legacy
  // free-text input then.
  const instAddons = useQuery({
    queryKey: ["instance-addons", "names"],
    queryFn: () =>
      api<{ addons: { name: string; kind: string }[] }>("/api/instance-addons/names"),
    enabled: open,
    staleTime: 30_000,
  });
  const availableInstanceNames = (instAddons.data?.addons ?? [])
    .filter((a) => a.kind === kind)
    .map((a) => a.name);

  useEffect(() => {
    if (open) {
      setKind("");
      setName("");
      setMode("managed");
      setExtSecret("");
      setExtKeys("");
      setInstName("");
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
    mutationFn: () => {
      const body: Parameters<typeof addAddon>[1] = { name, kind };
      if (mode === "external") {
        const keys = extKeys
          .split(/[,\s]+/)
          .map((k) => k.trim())
          .filter(Boolean);
        body.external = {
          secretName: extSecret.trim(),
          ...(keys.length ? { secretKeys: keys } : {}),
        };
      } else if (mode === "instance") {
        body.useInstanceAddon = instName.trim();
      }
      return addAddon(project, body);
    },
    onSuccess: () => {
      const verb =
        mode === "external" ? "connected" : mode === "instance" ? "provisioned" : "created";
      toast.success(`${addonLabel(kind)} addon "${name}" ${verb}`);
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
    if (mode === "external" && !extSecret.trim()) {
      toast.error("External: name an existing kube Secret to mirror");
      return;
    }
    if (mode === "instance" && !instName.trim()) {
      toast.error("Instance: name the registered shared addon");
      return;
    }
    if (mode === "instance" && kind !== "postgres") {
      toast.error("Instance-shared mode only supports postgres in v0.7.6");
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

            {/* Mode picker: kuso-managed / external / instance-shared.
                Three approaches to where the actual datastore lives.
                Managed = a fresh per-addon StatefulSet; External =
                connect to a managed cloud DB by mirroring its Secret;
                Instance = provision a database on an admin-registered
                shared server (Model 2). */}
            <div className="space-y-3 border-b border-[var(--border-subtle)] p-3">
              <div className="grid grid-cols-3 gap-1 rounded-md bg-[var(--bg-secondary)] p-0.5">
                {(["managed", "external", "instance"] as Mode[]).map((m) => {
                  const active = mode === m;
                  const labelMap: Record<Mode, string> = {
                    managed: "Managed",
                    external: "External",
                    instance: "Instance",
                  };
                  const subMap: Record<Mode, string> = {
                    managed: "kuso provisions",
                    external: "mirror a Secret",
                    instance: "shared server",
                  };
                  return (
                    <button
                      key={m}
                      type="button"
                      onClick={() => setMode(m)}
                      className={cn(
                        "flex flex-col items-start rounded-sm px-2 py-1.5 text-left transition-colors",
                        active
                          ? "bg-[var(--bg-elevated)] shadow-[var(--shadow-sm)]"
                          : "hover:bg-[var(--bg-tertiary)]/50"
                      )}
                    >
                      <span className="text-[12px] font-medium">{labelMap[m]}</span>
                      <span className="font-mono text-[9px] text-[var(--text-tertiary)]">{subMap[m]}</span>
                    </button>
                  );
                })}
              </div>

              {mode === "external" && (
                <div className="space-y-2">
                  <p className="text-[11px] leading-snug text-[var(--text-tertiary)]">
                    For managed Postgres / Redis (Hetzner Cloud, Neon, RDS,
                    Upstash). kuso mirrors the Secret's keys into{" "}
                    <code className="font-mono">{name || "<name>"}-conn</code>.
                  </p>
                  <div>
                    <label
                      htmlFor="addon-ext-secret"
                      className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
                    >
                      source secret
                    </label>
                    <Input
                      id="addon-ext-secret"
                      value={extSecret}
                      onChange={(e) => setExtSecret(e.target.value)}
                      placeholder="hetzner-pg-creds"
                      className="mt-1 h-7 font-mono text-[11px]"
                      spellCheck={false}
                    />
                  </div>
                  <div>
                    <label
                      htmlFor="addon-ext-keys"
                      className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
                    >
                      keys (optional, comma-separated)
                    </label>
                    <Input
                      id="addon-ext-keys"
                      value={extKeys}
                      onChange={(e) => setExtKeys(e.target.value)}
                      placeholder="DATABASE_URL, POSTGRES_HOST"
                      className="mt-1 h-7 font-mono text-[11px]"
                      spellCheck={false}
                    />
                    <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                      Empty = mirror every key from the source.
                    </p>
                  </div>
                </div>
              )}

              {mode === "instance" && (
                <div className="space-y-2">
                  <p className="text-[11px] leading-snug text-[var(--text-tertiary)]">
                    Provisions an isolated database on a shared server
                    registered cluster-wide. v0.7.6: postgres only.
                    Admin must first set{" "}
                    <code className="font-mono">INSTANCE_ADDON_&lt;NAME&gt;_DSN_ADMIN</code> in instance secrets.
                  </p>
                  <div>
                    <label
                      htmlFor="addon-inst-name"
                      className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
                    >
                      instance addon
                    </label>
                    {availableInstanceNames.length > 0 ? (
                      <select
                        id="addon-inst-name"
                        value={instName}
                        onChange={(e) => setInstName(e.target.value)}
                        className="mt-1 h-7 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px] text-[var(--text-primary)]"
                      >
                        <option value="">(pick one)</option>
                        {availableInstanceNames.map((n) => (
                          <option key={n} value={n}>
                            {n}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <Input
                        id="addon-inst-name"
                        value={instName}
                        onChange={(e) => setInstName(e.target.value)}
                        placeholder="pg"
                        className="mt-1 h-7 font-mono text-[11px]"
                        spellCheck={false}
                      />
                    )}
                    <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
                      {availableInstanceNames.length > 0
                        ? `${availableInstanceNames.length} ${kind} server${availableInstanceNames.length === 1 ? "" : "s"} registered for this instance.`
                        : `No ${kind} shared servers registered. Ask an admin or type the name manually.`}
                    </p>
                  </div>
                </div>
              )}
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
                {create.isPending
                  ? mode === "external"
                    ? "Connecting…"
                    : mode === "instance"
                      ? "Provisioning…"
                      : "Creating…"
                  : mode === "external"
                    ? "Connect addon"
                    : mode === "instance"
                      ? "Provision DB"
                      : "Add addon"}
              </Button>
            </footer>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
