"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { Database, Cloud, Server, Trash2, RotateCw, CheckCircle2, AlertCircle, Loader2, Plus } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";

// /settings/database — first-class home for the cluster-shared
// Postgres. One of three states:
//
//   * none      — nothing configured. User picks "provision on-cluster"
//                 (kuso manages a StatefulSet) or "use external" (paste
//                 a DSN to an off-cluster PG).
//   * managed   — kuso runs a Postgres instance on this cluster via
//                 the kusoaddon helm chart. We own its lifecycle and
//                 surface the phase (pending / provisioning / ready /
//                 failed). The actual conn details are harvested by
//                 the server's background reconciler.
//   * external  — admin pasted a DSN. We show host/user/port and the
//                 count of consumer projects.
//
// All three states are the same instance-shared admin DSN under the
// hood. The mode field tells us which UI to render. Once configured,
// projects opt-in via the Add Addon dialog → instance mode.

interface Status {
  mode: "none" | "managed" | "external";
  phase?: "provisioning" | "ready" | "failed" | "unhealthy";
  host?: string;
  port?: string;
  user?: string;
  version?: string;
  size?: string;
  ha?: boolean;
  storageSize?: string;
  projectsUsing: number;
  lastError?: string;
}

export default function DatabasePage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();

  // Poll every 5s while in a transient phase ("provisioning") so the
  // UI flips to "ready" within one tick of the cluster catching up.
  // Steady states (ready/failed/none/external) refetch on focus only.
  const status = useQuery<Status>({
    queryKey: ["instance-pg", "status"],
    queryFn: () => api("/api/instance-pg"),
    enabled: isAdmin,
    refetchInterval: (q) => {
      const d = q.state.data;
      return d?.phase === "provisioning" ? 5_000 : false;
    },
  });

  const provision = useMutation({
    mutationFn: (body: { size: string; ha: boolean; storageSize: string; version: string }) =>
      api("/api/instance-pg/managed", { method: "POST", body }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["instance-pg", "status"] });
      toast.success("Provisioning started — Postgres will be ready in ~1 minute.");
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Provision failed"),
  });
  const configureExt = useMutation({
    mutationFn: (dsn: string) => api("/api/instance-pg/external", { method: "POST", body: { dsn } }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["instance-pg", "status"] });
      toast.success("External Postgres connected.");
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Connection failed"),
  });
  const disable = useMutation({
    mutationFn: () => api("/api/instance-pg", { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["instance-pg", "status"] });
      toast.success("Disabled.");
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Disable failed"),
  });

  const [showProvisionDialog, setShowProvisionDialog] = useState(false);
  const [showExternalDialog, setShowExternalDialog] = useState(false);
  const [showDisableConfirm, setShowDisableConfirm] = useState(false);

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
          Cluster database configuration is admin-only.
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Cluster database</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          One Postgres serves every project that opts in. Each project gets its own logical database + user inside it; no project sees another&apos;s data.
        </p>
      </header>

      {status.isPending && <Skeleton className="h-40 w-full rounded-md" />}
      {status.isError && (
        <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-400">
          {(status.error as Error)?.message ?? "Failed to load cluster database status."}
        </div>
      )}
      {status.data && status.data.mode === "none" && (
        <NotConfiguredCard
          onPickManaged={() => setShowProvisionDialog(true)}
          onPickExternal={() => setShowExternalDialog(true)}
        />
      )}
      {status.data && status.data.mode === "managed" && (
        <ManagedCard
          status={status.data}
          onDisable={() => setShowDisableConfirm(true)}
        />
      )}
      {status.data && status.data.mode === "external" && (
        <ExternalCard
          status={status.data}
          onDisable={() => setShowDisableConfirm(true)}
        />
      )}

      {/* Additional shared servers — the named registry. The primary
          slot above is the default cluster DB; these are extra servers
          (a second on-cluster PG, or external Neon/RDS/Supabase) that
          projects can also opt into by name. */}
      <AdditionalServers />

      <ProvisionDialog
        open={showProvisionDialog}
        onClose={() => setShowProvisionDialog(false)}
        onSubmit={(body) => {
          provision.mutate(body, {
            onSettled: () => setShowProvisionDialog(false),
          });
        }}
        submitting={provision.isPending}
      />
      <ExternalDialog
        open={showExternalDialog}
        onClose={() => setShowExternalDialog(false)}
        onSubmit={(dsn) => {
          configureExt.mutate(dsn, {
            onSuccess: () => setShowExternalDialog(false),
          });
        }}
        submitting={configureExt.isPending}
      />
      <ConfirmDialog
        open={showDisableConfirm}
        onCancel={() => setShowDisableConfirm(false)}
        title="Disable the cluster database?"
        body={
          status.data?.projectsUsing && status.data.projectsUsing > 0
            ? `${status.data.projectsUsing} project(s) currently use this database. You must remove their addons first.`
            : status.data?.mode === "managed"
              ? "This deletes the on-cluster Postgres StatefulSet. ALL data is destroyed. There is no undo."
              : "Disconnect the external Postgres. Projects will lose their database connection on the next pod reconcile."
        }
        destructive
        confirmLabel="Disable"
        onConfirm={() => {
          disable.mutate();
          setShowDisableConfirm(false);
        }}
      />
    </div>
  );
}

// NotConfiguredCard is the empty-state. Two equal-weight CTAs so the
// user picks based on their constraints rather than being steered.
function NotConfiguredCard({
  onPickManaged,
  onPickExternal,
}: {
  onPickManaged: () => void;
  onPickExternal: () => void;
}) {
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <button
        type="button"
        onClick={onPickManaged}
        className="group rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 text-left transition-colors hover:border-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Server className="h-5 w-5 text-[var(--text-secondary)] transition-colors group-hover:text-[var(--accent)]" />
        <h3 className="mt-2 text-sm font-semibold tracking-tight">Run on this cluster</h3>
        <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
          Kuso provisions a Postgres StatefulSet in this cluster. One-click; uses cluster storage.
        </p>
        <p className="mt-3 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          recommended for self-hosted
        </p>
      </button>
      <button
        type="button"
        onClick={onPickExternal}
        className="group rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 text-left transition-colors hover:border-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Cloud className="h-5 w-5 text-[var(--text-secondary)] transition-colors group-hover:text-[var(--accent)]" />
        <h3 className="mt-2 text-sm font-semibold tracking-tight">Point at an external Postgres</h3>
        <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
          Use Neon, RDS, Supabase, an EC2 box — anything reachable from this cluster. Paste a DSN.
        </p>
        <p className="mt-3 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          managed providers / shared infra
        </p>
      </button>
    </div>
  );
}

function ManagedCard({ status, onDisable }: { status: Status; onDisable: () => void }) {
  const phaseColor =
    status.phase === "ready"
      ? "text-emerald-400"
      : status.phase === "failed" || status.phase === "unhealthy"
        ? "text-red-400"
        : "text-amber-400";
  const PhaseIcon =
    status.phase === "ready"
      ? CheckCircle2
      : status.phase === "failed" || status.phase === "unhealthy"
        ? AlertCircle
        : Loader2;
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-2">
          <Server className="h-4 w-4 text-[var(--text-secondary)]" />
          <h2 className="text-sm font-semibold tracking-tight">On-cluster Postgres</h2>
        </div>
        <span className={`inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-widest ${phaseColor}`}>
          <PhaseIcon className={`h-3 w-3 ${status.phase === "provisioning" ? "animate-spin" : ""}`} />
          {status.phase ?? "unknown"}
        </span>
      </div>
      {status.phase === "provisioning" && (
        <p className="mt-3 rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-[12px] text-amber-300">
          Helm-installing the Postgres chart. Usually ready in 30–90 seconds — this page polls every 5s.
        </p>
      )}
      {status.phase === "failed" && status.lastError && (
        <p className="mt-3 rounded-md border border-red-500/30 bg-red-500/5 p-3 font-mono text-[11px] leading-snug text-red-300">
          {status.lastError}
        </p>
      )}
      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1.5 text-[11px]">
        {status.host && <Field name="host" value={`${status.host}${status.port ? `:${status.port}` : ""}`} />}
        {status.user && <Field name="user" value={status.user} />}
        {status.version && <Field name="version" value={status.version} />}
        {status.size && <Field name="size" value={status.size} />}
        {status.storageSize && <Field name="storage" value={status.storageSize} />}
        <Field name="ha" value={status.ha ? "yes (CNPG, 3 replicas)" : "no (single-node)"} />
        <Field name="projects connected" value={String(status.projectsUsing)} />
      </dl>
      <div className="mt-4 flex justify-end">
        <Button variant="outline" size="sm" onClick={onDisable} className="gap-1.5 text-red-400 hover:text-red-300">
          <Trash2 className="h-3 w-3" />
          Disable + delete
        </Button>
      </div>
    </div>
  );
}

function ExternalCard({ status, onDisable }: { status: Status; onDisable: () => void }) {
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-2">
          <Cloud className="h-4 w-4 text-[var(--text-secondary)]" />
          <h2 className="text-sm font-semibold tracking-tight">External Postgres</h2>
        </div>
        <span className="inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-widest text-emerald-400">
          <CheckCircle2 className="h-3 w-3" />
          connected
        </span>
      </div>
      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1.5 text-[11px]">
        {status.host && <Field name="host" value={`${status.host}${status.port ? `:${status.port}` : ""}`} />}
        {status.user && <Field name="user" value={status.user} />}
        <Field name="projects connected" value={String(status.projectsUsing)} />
      </dl>
      <div className="mt-4 flex justify-end">
        <Button variant="outline" size="sm" onClick={onDisable} className="gap-1.5">
          <Trash2 className="h-3 w-3" />
          Disconnect
        </Button>
      </div>
    </div>
  );
}

function Field({ name, value }: { name: string; value: string }) {
  return (
    <div className="contents">
      <dt className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">{name}</dt>
      <dd className="truncate font-mono text-[var(--text-secondary)]">{value}</dd>
    </div>
  );
}

function ProvisionDialog({
  open,
  onClose,
  onSubmit,
  submitting,
}: {
  open: boolean;
  onClose: () => void;
  onSubmit: (body: { size: string; ha: boolean; storageSize: string; version: string }) => void;
  submitting: boolean;
}) {
  const [size, setSize] = useState("small");
  const [ha, setHa] = useState(false);
  const [storageSize, setStorageSize] = useState("20Gi");
  const [version, setVersion] = useState("16");
  if (!open) return null;
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-md rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-5 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="text-sm font-semibold tracking-tight">Provision on-cluster Postgres</h2>
        <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
          Kuso installs a Postgres StatefulSet in the cluster namespace. Projects opt in later via the Add Addon dialog.
        </p>
        <div className="mt-4 space-y-3">
          <Picker label="size" value={size} onChange={setSize} options={["small", "medium", "large"]} />
          <div>
            <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">version</label>
            <Input value={version} onChange={(e) => setVersion(e.target.value)} className="mt-1 h-7 font-mono text-[11px]" placeholder="16" />
          </div>
          <div>
            <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">storage size</label>
            <Input value={storageSize} onChange={(e) => setStorageSize(e.target.value)} className="mt-1 h-7 font-mono text-[11px]" placeholder="20Gi" />
          </div>
          <label className="flex items-center gap-2 font-mono text-[11px] text-[var(--text-secondary)]">
            <input type="checkbox" checked={ha} onChange={(e) => setHa(e.target.checked)} className="h-3.5 w-3.5" />
            High availability (CNPG, 3 replicas — requires the CNPG operator installed cluster-wide)
          </label>
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>Cancel</Button>
          <Button size="sm" disabled={submitting} onClick={() => onSubmit({ size, ha, storageSize, version })}>
            {submitting ? "Provisioning…" : "Provision"}
          </Button>
        </div>
      </div>
    </div>
  );
}

function ExternalDialog({
  open,
  onClose,
  onSubmit,
  submitting,
}: {
  open: boolean;
  onClose: () => void;
  onSubmit: (dsn: string) => void;
  submitting: boolean;
}) {
  const [dsn, setDsn] = useState("");
  if (!open) return null;
  const valid = /^postgres(ql)?:\/\//.test(dsn.trim());
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-md rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-5 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="text-sm font-semibold tracking-tight">Connect external Postgres</h2>
        <p className="mt-1 text-[12px] text-[var(--text-secondary)]">
          Paste a superuser DSN. Kuso will test the connection (SELECT 1) before saving. The password never leaves this server.
        </p>
        <div className="mt-4">
          <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">DSN</label>
          <Input
            value={dsn}
            onChange={(e) => setDsn(e.target.value)}
            className="mt-1 h-7 font-mono text-[11px]"
            placeholder="postgres://admin:pw@host.example.com:5432/postgres?sslmode=disable"
            spellCheck={false}
            autoFocus
          />
          <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
            Needs CREATEDB + CREATEROLE so kuso can carve per-project databases.
          </p>
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>Cancel</Button>
          <Button size="sm" disabled={!valid || submitting} onClick={() => onSubmit(dsn.trim())}>
            {submitting ? (
              <span className="inline-flex items-center gap-1.5">
                <RotateCw className="h-3 w-3 animate-spin" />
                Testing…
              </span>
            ) : (
              "Test + connect"
            )}
          </Button>
        </div>
      </div>
    </div>
  );
}

function Picker({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: string[];
}) {
  return (
    <div>
      <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">{label}</label>
      <div className="mt-1 flex gap-1">
        {options.map((o) => (
          <button
            key={o}
            type="button"
            onClick={() => onChange(o)}
            className={`flex-1 rounded-md border px-2 py-1 font-mono text-[11px] transition-colors ${
              value === o
                ? "border-[var(--accent)] bg-[var(--accent)]/10 text-[var(--accent)]"
                : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-secondary)] hover:border-[var(--border-strong)]"
            }`}
          >
            {o}
          </button>
        ))}
      </div>
    </div>
  );
}

interface InstanceAddon {
  name: string;
  host: string;
  port?: string;
  user?: string;
  kind: string;
}

// AdditionalServers surfaces the named instance-addon registry: extra
// shared database servers (a second on-cluster PG, or an external
// Neon/RDS/Supabase) registered by superuser DSN. Projects opt into one
// by name via the canvas Add Addon dialog → Instance. The primary
// managed/external slot above auto-registers as "pg", so we filter it
// out here to avoid showing it twice — it's the card at the top.
function AdditionalServers() {
  const qc = useQueryClient();
  const list = useQuery<{ addons: InstanceAddon[] }>({
    queryKey: ["instance-addons"],
    queryFn: () => api("/api/instance-addons"),
  });
  const register = useMutation({
    mutationFn: ({ name, dsn }: { name: string; dsn: string }) =>
      api("/api/instance-addons", { method: "PUT", body: { name, dsn } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["instance-addons"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Register failed"),
  });
  const unregister = useMutation({
    mutationFn: (name: string) =>
      api(`/api/instance-addons/${encodeURIComponent(name)}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["instance-addons"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Unregister failed"),
  });

  const [showAdd, setShowAdd] = useState(false);
  const [newName, setNewName] = useState("");
  const [newDSN, setNewDSN] = useState("");
  const [pendingUnregister, setPendingUnregister] = useState<string | null>(null);

  const onSubmit = () => {
    const name = newName.trim().toLowerCase();
    const dsn = newDSN.trim();
    if (!name || !dsn) return;
    if (!/^[a-z][a-z0-9-]{0,30}[a-z0-9]?$/.test(name)) {
      toast.error("Name: lowercase, dashes, ≤32 chars, must start with a letter");
      return;
    }
    if (!/^postgres(ql)?:\/\//.test(dsn)) {
      toast.error("DSN must start with postgres:// or postgresql://");
      return;
    }
    if (name === "pg") {
      toast.error('"pg" is reserved for the primary cluster database above');
      return;
    }
    register.mutate(
      { name, dsn },
      {
        onSuccess: () => {
          toast.success(`${name} registered — projects can now connect to it`);
          setNewName("");
          setNewDSN("");
          setShowAdd(false);
        },
      }
    );
  };

  // The primary slot registers itself as "pg"; it's the card above, so
  // drop it from this list.
  const addons = (list.data?.addons ?? [])
    .filter((a) => a.name !== "pg")
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name));

  return (
    <section className="mt-8 space-y-3">
      <header className="flex items-baseline justify-between">
        <div>
          <h2 className="text-sm font-semibold tracking-tight">Additional shared servers</h2>
          <p className="mt-1 text-[12px] leading-relaxed text-[var(--text-secondary)]">
            Register extra Postgres servers — a second on-cluster instance, or an external
            Neon / RDS / Supabase — by superuser DSN. Projects opt into one by name via{" "}
            <code className="rounded bg-[var(--bg-secondary)] px-1 font-mono text-[11px]">
              + addon → Instance
            </code>{" "}
            on the canvas; kuso carves an isolated database + role per project.
          </p>
        </div>
        {!showAdd && (
          <Button size="sm" variant="ghost" onClick={() => setShowAdd(true)} className="shrink-0">
            <Plus className="h-3.5 w-3.5" />
            Register
          </Button>
        )}
      </header>

      {list.isPending ? (
        <Skeleton className="h-16 w-full" />
      ) : addons.length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
          No additional servers. The primary cluster database above covers most setups; register
          more only if you need separate instances or an external provider.
        </p>
      ) : (
        <ul className="space-y-2">
          {addons.map((a) => (
            <li
              key={a.name}
              className="flex items-center gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-2"
            >
              <Database className="h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
              <div className="min-w-0 flex-1">
                <div className="flex items-baseline gap-2">
                  <span className="font-mono text-[13px] font-medium text-[var(--text-primary)]">{a.name}</span>
                  <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                    {a.kind}
                  </span>
                </div>
                <p className="mt-0.5 truncate font-mono text-[10px] text-[var(--text-tertiary)]">
                  {a.user ? `${a.user}@` : ""}
                  {a.host || "<unparseable host>"}
                  {a.port ? `:${a.port}` : ""}
                </p>
              </div>
              <button
                type="button"
                onClick={() => setPendingUnregister(a.name)}
                disabled={unregister.isPending}
                className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400 disabled:opacity-40"
                aria-label={`Unregister ${a.name}`}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </button>
            </li>
          ))}
        </ul>
      )}

      {showAdd && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit();
          }}
          className="flex flex-col gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3"
        >
          <div>
            <label
              htmlFor="add-server-name"
              className="mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
            >
              name
            </label>
            <Input
              id="add-server-name"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder="analytics-pg"
              className="h-8 font-mono text-[12px]"
              spellCheck={false}
              autoComplete="off"
            />
            <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
              Lowercase, dashes, ≤32 chars. Projects reference this name when opting in.
            </p>
          </div>
          <div>
            <label
              htmlFor="add-server-dsn"
              className="mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
            >
              superuser dsn
            </label>
            <Input
              id="add-server-dsn"
              value={newDSN}
              onChange={(e) => setNewDSN(e.target.value)}
              type="password"
              placeholder="postgres://admin:pw@db.example.com:5432/postgres?sslmode=require"
              className="h-8 font-mono text-[12px]"
              spellCheck={false}
              autoComplete="off"
            />
            <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
              Needs CREATE DATABASE + CREATE ROLE. The password never round-trips back to the browser.
            </p>
          </div>
          <div className="flex items-center justify-end gap-2">
            <Button
              size="sm"
              variant="ghost"
              type="button"
              onClick={() => {
                setShowAdd(false);
                setNewName("");
                setNewDSN("");
              }}
              disabled={register.isPending}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              type="submit"
              disabled={!newName.trim() || !newDSN.trim() || register.isPending}
            >
              <Plus className="h-3.5 w-3.5" />
              {register.isPending ? "Registering…" : "Register"}
            </Button>
          </div>
        </form>
      )}

      <ConfirmDialog
        open={pendingUnregister !== null}
        title="Unregister shared server?"
        body={
          <p>
            Projects that already opted in to{" "}
            <span className="font-mono text-[var(--text-primary)]">{pendingUnregister}</span> keep
            their per-project databases and connection secrets, but no new projects can opt in until
            you re-register. The DSN is removed from the kuso-instance-shared Secret.
          </p>
        }
        typeToConfirm={pendingUnregister ?? undefined}
        confirmLabel="Unregister"
        destructive
        pending={unregister.isPending}
        onConfirm={() => {
          if (pendingUnregister) unregister.mutate(pendingUnregister);
          setPendingUnregister(null);
        }}
        onCancel={() => setPendingUnregister(null)}
      />
    </section>
  );
}

// Tag the icon for module re-use; suppresses "unused" warning in
// builds where Database import becomes incidental.
export const _databaseIcon = Database;
