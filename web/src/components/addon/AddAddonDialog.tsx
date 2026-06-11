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
// Order = grid order; put implemented kinds first, reserved-only ones
// last so the picker leads with what works.
const KINDS = [
  // Implemented (real chart, real workload).
  "postgres",
  "redis",
  "s3",
  "mailpit",
  "nats",
  "meilisearch",
  "clickhouse",
  "redpanda",
  // Reserved — chart renders the unsupported marker. Hidden behind a
  // flag once we have a Coming-Soon affordance; for now they're kept
  // out of the picker entirely so users can't add them by accident.
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
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [version, setVersion] = useState<string>("");
  const [ha, setHA] = useState(false);
  const [storageSize, setStorageSize] = useState<string>("");
  // requireTLS opts a managed postgres addon into in-cluster wire TLS
  // (spec.tls=require) so its conn string advertises sslmode=require —
  // for apps that mandate encrypted DB connections in production.
  const [requireTLS, setRequireTLS] = useState(false);
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
      setShowAdvanced(false);
      setVersion("");
      setHA(false);
      setStorageSize("");
      setRequireTLS(false);
    }
  }, [open]);

  // When the user picks postgres AND there's a cluster-shared PG
  // registered, default to "instance" mode with the addon pre-selected.
  // That's the one-click path: pick "postgres" → see the form already
  // pointed at the cluster PG, just click Create. The user can still
  // flip back to managed (own StatefulSet) if they want isolation.
  //
  // Skipped for non-postgres kinds since instance mode is pg-only.
  useEffect(() => {
    if (!open) return;
    if (kind !== "postgres") return;
    if (availableInstanceNames.length === 0) return;
    // Only auto-flip on initial kind-pick. If the user already moved
    // off "managed" we respect that choice — don't ping-pong their
    // mode on every re-render.
    if (mode !== "managed") return;
    setMode("instance");
    // Prefer "pg" if registered; else use whatever's first.
    const preferred = availableInstanceNames.includes("pg") ? "pg" : availableInstanceNames[0];
    setInstName(preferred);
    // We intentionally exclude `mode` from deps: re-running the effect
    // every time the user toggles mode would lock them back into
    // "instance" the moment they tried to pick another mode. The
    // length-only dep on availableInstanceNames is enough — by the
    // time names change, we want to re-evaluate.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, open, availableInstanceNames.length]);

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
      if (mode === "managed") {
        if (version.trim()) body.version = version.trim();
        if (ha) body.ha = true;
        if (storageSize.trim()) body.storageSize = storageSize.trim();
        if (kind === "postgres" && requireTLS) body.tls = "require";
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
                  // Instance-shared mode is postgres-only today (chart
                  // emits the conn-secret bridge only for that kind).
                  // Disable the chip rather than waiting for a submit-
                  // time toast.
                  const disabled = m === "instance" && kind !== "" && kind !== "postgres";
                  return (
                    <button
                      key={m}
                      type="button"
                      onClick={() => !disabled && setMode(m)}
                      disabled={disabled}
                      title={disabled ? "Instance-shared mode is postgres-only" : undefined}
                      className={cn(
                        "flex flex-col items-start rounded-sm px-2 py-1.5 text-left transition-colors",
                        active
                          ? "bg-[var(--bg-elevated)] shadow-[var(--shadow-sm)]"
                          : "hover:bg-[var(--bg-tertiary)]/50",
                        disabled && "cursor-not-allowed opacity-40 hover:bg-transparent"
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

              {mode === "managed" && kind && (
                <div className="space-y-2 border-t border-[var(--border-subtle)] pt-3">
                  <button
                    type="button"
                    onClick={() => setShowAdvanced((v) => !v)}
                    className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                  >
                    {showAdvanced ? "▾" : "▸"} advanced
                  </button>
                  {showAdvanced && (
                    <div className="space-y-2">
                      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
                        These default to the chart's recommended values.
                        <span className="text-amber-400"> Storage size cannot be reduced after create.</span>
                      </p>
                      <div className="grid grid-cols-2 gap-2">
                        <div>
                          <label
                            htmlFor="addon-version"
                            className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
                          >
                            version
                          </label>
                          <Input
                            id="addon-version"
                            value={version}
                            onChange={(e) => setVersion(e.target.value)}
                            placeholder="(chart default)"
                            className="mt-1 h-7 font-mono text-[11px]"
                            spellCheck={false}
                          />
                        </div>
                        <div>
                          <label
                            htmlFor="addon-storage"
                            className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
                          >
                            storage size
                          </label>
                          <Input
                            id="addon-storage"
                            value={storageSize}
                            onChange={(e) => setStorageSize(e.target.value)}
                            placeholder="10Gi"
                            className="mt-1 h-7 font-mono text-[11px]"
                            spellCheck={false}
                          />
                        </div>
                      </div>
                      <label className="flex items-center gap-2 font-mono text-[11px] text-[var(--text-secondary)]">
                        <input
                          type="checkbox"
                          checked={ha}
                          onChange={(e) => setHA(e.target.checked)}
                          className="h-3.5 w-3.5"
                        />
                        High availability (multi-replica + anti-affinity)
                      </label>
                      {kind === "postgres" && (
                        <div>
                          <label className="flex items-center gap-2 font-mono text-[11px] text-[var(--text-secondary)]">
                            <input
                              type="checkbox"
                              checked={requireTLS}
                              onChange={(e) => setRequireTLS(e.target.checked)}
                              className="h-3.5 w-3.5"
                            />
                            Require TLS on the wire (sslmode=require)
                          </label>
                          <p className="mt-1 pl-5 font-mono text-[10px] leading-relaxed text-[var(--text-tertiary)]">
                            Default is plaintext in-cluster (sslmode=disable) — works with every
                            driver. Enable only if your app mandates encrypted DB connections
                            (Go/pgx, Rails); note default node-postgres rejects the self-signed
                            cert under require.
                          </p>
                        </div>
                      )}
                    </div>
                  )}
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
