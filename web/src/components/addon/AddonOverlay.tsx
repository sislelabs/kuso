"use client";

import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import {
  useAddons,
  addonSecret,
  deleteAddon,
  listBackups,
  restoreBackup,
  listSQLTables,
  runSQL,
  setAddonPlacement,
  type BackupObject,
} from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api-client";
import { AddonIcon, addonLabel } from "@/components/addon/AddonIcon";
import { useCan, Perms } from "@/features/auth";
import { X, RotateCcw, Trash2, Database, HardDrive, Settings, Info, Play, Copy, Eye, EyeOff, Check, MapPin, Plus } from "lucide-react";
import type { NodeSummary } from "@/components/layout/ServersPopover";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";

type Tab = "overview" | "backups" | "sql" | "settings";
const TABS: { id: Tab; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { id: "overview", label: "Overview", icon: Info },
  { id: "backups",  label: "Backups",  icon: HardDrive },
  { id: "sql",      label: "SQL",      icon: Database },
  { id: "settings", label: "Settings", icon: Settings },
];

interface Props {
  project: string;
  addon: string | null;
  onClose: () => void;
}

// AddonOverlay mirrors ServiceOverlay: a right-side slide-in panel
// scoped to one addon, with tabs for the operations the user
// actually does on a postgres (browse, restore, delete). Backups +
// SQL tabs hide themselves for non-postgres addons since the
// endpoints they hit are postgres-only today.
export function AddonOverlay({ project, addon, onClose }: Props) {
  const open = !!addon;
  const [tab, setTab] = useState<Tab>("overview");
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (addon) setTab("overview");
  }, [addon]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, onClose]);

  const addons = useAddons(project);
  const data = (addons.data ?? []).find((a) => a.metadata.name === addon);
  const kind = data?.spec.kind ?? "";
  const isPostgres = kind === "postgres";
  const canSQL = useCan(Perms.SQLRead);
  const canWriteAddon = useCan(Perms.AddonsWrite);

  return (
    <AnimatePresence>
      {open && (
        <div className="fixed inset-0 z-50 flex" role="dialog" aria-modal="true">
          <motion.button
            type="button"
            aria-label="Close"
            onClick={onClose}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="absolute inset-0 bg-[rgba(8,8,11,0.55)] backdrop-blur-[2px]"
          />
          <motion.div
            ref={panelRef}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ type: "spring", stiffness: 320, damping: 34, mass: 0.8 }}
            className="relative z-10 ml-auto flex h-full w-full max-w-3xl flex-col border-l border-[var(--border-subtle)] bg-[var(--bg-primary)] shadow-[var(--shadow-lg)]"
          >
            <header className="flex shrink-0 items-start gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-5 py-4">
              <span className="mt-0.5 inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-primary)]">
                <AddonIcon kind={kind} />
              </span>
              <div className="min-w-0 flex-1">
                <h2 className="font-heading text-lg font-semibold tracking-tight truncate">
                  {addon ?? ""}
                </h2>
                <div className="mt-1 flex flex-wrap items-center gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
                  <span className="uppercase tracking-widest">{addonLabel(kind)}</span>
                  <span>·</span>
                  <span>project {project}</span>
                </div>
              </div>
              <button
                type="button"
                onClick={onClose}
                aria-label="Close"
                className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              >
                <X className="h-4 w-4" />
              </button>
            </header>

            <nav className="flex shrink-0 items-center gap-1 border-b border-[var(--border-subtle)] px-3">
              {TABS.map((t) => {
                if (!isPostgres && (t.id === "backups" || t.id === "sql")) return null;
                // SQL tab needs sql:read; Settings (delete) needs
                // addons:write. Hide entirely for users without —
                // less confusing than rendering then 403'ing on action.
                if (t.id === "sql" && !canSQL) return null;
                if (t.id === "settings" && !canWriteAddon) return null;
                const active = t.id === tab;
                return (
                  <button
                    key={t.id}
                    type="button"
                    onClick={() => setTab(t.id)}
                    className={cn(
                      "relative inline-flex h-10 items-center gap-1.5 px-3 text-sm font-medium transition-colors",
                      active
                        ? "text-[var(--text-primary)]"
                        : "text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
                    )}
                  >
                    <t.icon className="h-3.5 w-3.5" />
                    {t.label}
                    {active && (
                      <motion.span
                        layoutId="addon-tab-underline"
                        className="absolute inset-x-3 bottom-0 h-[2px] rounded-full bg-[var(--text-primary)]"
                        transition={{ type: "spring", stiffness: 380, damping: 32 }}
                      />
                    )}
                  </button>
                );
              })}
            </nav>

            <div className="min-h-0 flex-1 overflow-y-auto">
              {!data ? (
                <div className="space-y-3 p-6">
                  <Skeleton className="h-8 w-48" />
                  <Skeleton className="h-32 w-full" />
                </div>
              ) : tab === "overview" ? (
                <OverviewTab project={project} addon={addon!} kind={kind} cr={data} />
              ) : tab === "backups" ? (
                <BackupsTab project={project} addon={addon!} />
              ) : tab === "sql" ? (
                <SQLTab project={project} addon={addon!} />
              ) : (
                <SettingsTab project={project} addon={addon!} cr={data} onClose={onClose} />
              )}
            </div>
          </motion.div>
        </div>
      )}
    </AnimatePresence>
  );
}

// ---------- Tabs ----------

// storageSizeFromSpec mirrors the helm chart's kusoaddon.storageSize
// helper. spec.storageSize (explicit) wins over the t-shirt size mapping.
function storageSizeFromSpec(spec: { storageSize?: string; size?: "small" | "medium" | "large" } | undefined): string {
  if (spec?.storageSize) return spec.storageSize;
  switch (spec?.size) {
    case "medium":
      return "20Gi";
    case "large":
      return "100Gi";
    default:
      return "5Gi";
  }
}

function OverviewTab({ project, addon, kind, cr }: { project: string; addon: string; kind: string; cr?: import("@/types/projects").KusoAddon }) {
  const canReadSecrets = useCan(Perms.SecretsRead);
  // The connection secret is provisioned async by helm-operator; it
  // can take a few seconds after the addon is created. Refetch slowly
  // when it isn't ready yet so the panel auto-fills.
  const conn = useQuery({
    queryKey: ["addons", project, addon, "secret"],
    queryFn: () => addonSecret(project, addon),
    enabled: canReadSecrets,
    refetchInterval: (q) => (q.state.data ? false : 5_000),
  });
  const storageSize = storageSizeFromSpec(cr?.spec);
  const tier = cr?.spec.size ?? "small";
  return (
    <div className="space-y-4 p-5">
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <Row label="kind" value={kind || "—"} />
        <Row label="release" value={addon} />
        <Row
          label="storage"
          value={
            <span className="font-mono text-[12px] text-[var(--text-secondary)]">
              {storageSize}
              <span className="ml-2 text-[var(--text-tertiary)]">· tier {tier}</span>
            </span>
          }
          last
        />
      </section>

      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        Data persists on the cluster node&apos;s disk via a PVC. Survives pod
        restarts, deployments, and helm upgrades. Does NOT survive
        node failure or addon deletion. Configure scheduled backups in{" "}
        <a href="/settings/backups" className="text-[var(--accent)] underline">
          /settings/backups
        </a>{" "}
        for off-cluster snapshots.
      </p>

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
          <h3 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Connection
          </h3>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            wired into every service in this project
          </span>
        </header>
        {!canReadSecrets ? (
          <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
            need{" "}
            <span className="text-[var(--text-secondary)]">secrets:read</span>{" "}
            to view connection details — your role doesn&apos;t carry it.
          </p>
        ) : conn.isPending ? (
          <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">loading…</p>
        ) : conn.isError ? (
          <p className="px-3 py-3 font-mono text-[11px] text-amber-400">
            {conn.error instanceof Error ? conn.error.message : "load failed"}
          </p>
        ) : Object.keys(conn.data?.values ?? {}).length === 0 ? (
          <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
            connection secret not generated yet — give helm-operator a few seconds.
          </p>
        ) : (
          <ConnectionRows values={conn.data!.values} />
        )}
      </section>

      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        These vars are auto-injected as env on every service pod. Use them from your
        app code, or copy <span className="text-[var(--text-secondary)]">DATABASE_URL</span> to
        connect from <span className="font-mono">psql</span> /{" "}
        <span className="font-mono">kubectl port-forward</span>.
      </p>
    </div>
  );
}

// ConnectionRows renders the addon's connection secret as a list of
// key/value rows with copy + show/hide. Sensitive values (password,
// secret-looking keys, the URL) are masked by default; the user has to
// reveal explicitly. Sort: URL-style keys first, password last, the
// rest alphabetical — matches what people scan for when they want to
// connect.
function ConnectionRows({ values }: { values: Record<string, string> }) {
  const [shown, setShown] = useState<Record<string, boolean>>({});
  const [copied, setCopied] = useState<string | null>(null);
  const keys = Object.keys(values).sort((a, b) => {
    const score = (k: string) =>
      /url$/i.test(k) ? 0 : /password|secret|token/i.test(k) ? 2 : 1;
    const sa = score(a);
    const sb = score(b);
    return sa !== sb ? sa - sb : a.localeCompare(b);
  });
  const isSensitive = (k: string) => /url$|password|secret|token/i.test(k);
  const onCopy = async (k: string, v: string) => {
    try {
      await navigator.clipboard.writeText(v);
      setCopied(k);
      setTimeout(() => setCopied((c) => (c === k ? null : c)), 1200);
    } catch {
      toast.error("clipboard unavailable");
    }
  };
  return (
    <ul>
      {keys.map((k, i) => {
        const v = values[k];
        const sensitive = isSensitive(k);
        const visible = !sensitive || !!shown[k];
        return (
          <li
            key={k}
            className={cn(
              "grid grid-cols-[180px_1fr_auto] items-center gap-2 px-3 py-2",
              i < keys.length - 1 && "border-b border-[var(--border-subtle)]"
            )}
          >
            <span className="truncate font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              {k}
            </span>
            <span
              className={cn(
                "truncate rounded-md bg-[var(--bg-primary)] px-2 py-1 font-mono text-[11px]",
                visible ? "text-[var(--text-secondary)]" : "text-[var(--text-tertiary)] tracking-widest"
              )}
              title={visible ? v : "click eye to reveal"}
            >
              {visible ? v : "•".repeat(Math.min(v.length, 24))}
            </span>
            <div className="flex items-center gap-0.5">
              {sensitive && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  aria-label={visible ? "Hide" : "Show"}
                  onClick={() => setShown((s) => ({ ...s, [k]: !s[k] }))}
                >
                  {visible ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                </Button>
              )}
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                aria-label="Copy"
                onClick={() => onCopy(k, v)}
              >
                {copied === k ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
              </Button>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function Row({ label, value, last }: { label: string; value: React.ReactNode; last?: boolean }) {
  return (
    <div
      className={
        "flex items-center justify-between gap-3 px-3 py-2" +
        (!last ? " border-b border-[var(--border-subtle)]" : "")
      }
    >
      <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </span>
      <span className="font-mono text-[12px] text-[var(--text-secondary)]">{value}</span>
    </div>
  );
}

function BackupsTab({ project, addon }: { project: string; addon: string }) {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["addons", project, addon, "backups"],
    queryFn: () => listBackups(project, addon),
    refetchInterval: 30_000,
  });
  const restore = useMutation({
    mutationFn: (key: string) => restoreBackup(project, addon, key),
    onSuccess: (res) => {
      toast.success(`Restore job started: ${res.job}`);
      qc.invalidateQueries({ queryKey: ["addons", project, addon, "backups"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Restore failed"),
  });
  const [confirmKey, setConfirmKey] = useState<string | null>(null);

  if (list.isPending) {
    return <Skeleton className="m-5 h-40" />;
  }
  if (list.isError) {
    const msg = list.error instanceof Error ? list.error.message : "load failed";
    // 503 is the server's "S3 not configured" signal. Anything else
    // means the bucket is reachable but something is wrong (auth,
    // permissions, network) — those need a different message.
    const noS3 = msg.includes("503") || /s3|bucket|credentials/i.test(msg);
    return (
      <div className="m-5 rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
        {noS3 ? "Backups not set up yet" : `Backups unavailable: ${msg}`}
        <p className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
          {noS3 ? (
            <>
              Backups need S3 (or compatible) credentials. Add them in{" "}
              <a href="/settings/backups" className="text-[var(--accent)] underline">
                /settings/backups
              </a>
              , then put <span className="font-mono">backup.schedule</span> on this
              addon in <span className="font-mono">kuso.yml</span> to start the
              CronJob.
            </>
          ) : (
            <>
              Detail:{" "}
              <span className="font-mono text-[var(--text-secondary)]">{msg}</span>
            </>
          )}
        </p>
      </div>
    );
  }

  const items = list.data ?? [];
  if (items.length === 0) {
    return (
      <p className="m-5 rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
        No backups yet. The CronJob will drop one once its schedule fires.
      </p>
    );
  }
  // Newest first.
  const sorted = [...items].sort((a, b) =>
    (b.when ?? "").localeCompare(a.when ?? "")
  );

  return (
    <div className="p-5">
      <header className="mb-3 flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Backups</h3>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {items.length} {items.length === 1 ? "object" : "objects"} · auto-refresh 30s
        </span>
      </header>
      <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {sorted.map((b) => (
          <li
            key={b.key}
            className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-3 py-2 last:border-b-0"
          >
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-[12px] text-[var(--text-secondary)]">
                {tail(b.key)}
              </div>
              <div className="mt-0.5 flex items-center gap-3 font-mono text-[10px] text-[var(--text-tertiary)]">
                <span>{formatBytes(b.size)}</span>
                <span>·</span>
                <span title={b.when}>{b.when ? relativeTime(b.when) : "—"}</span>
              </div>
            </div>
            <Button
              size="sm"
              variant="outline"
              disabled={restore.isPending}
              onClick={() => setConfirmKey(b.key)}
            >
              <RotateCcw className="h-3 w-3" />
              Restore
            </Button>
          </li>
        ))}
      </ul>
      <ConfirmRestore
        item={sorted.find((b) => b.key === confirmKey) ?? null}
        pending={restore.isPending}
        onCancel={() => setConfirmKey(null)}
        onConfirm={(key) => {
          restore.mutate(key);
          setConfirmKey(null);
        }}
      />
    </div>
  );
}

function ConfirmRestore({
  item,
  pending,
  onCancel,
  onConfirm,
}: {
  item: BackupObject | null;
  pending: boolean;
  onCancel: () => void;
  onConfirm: (key: string) => void;
}) {
  return (
    <AnimatePresence>
      {item && (
        <motion.div
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.1 }}
          className="fixed inset-0 z-[60] flex items-center justify-center bg-[rgba(8,8,11,0.7)] p-4"
          onClick={onCancel}
        >
          <motion.div
            initial={{ scale: 0.96, y: 4 }}
            animate={{ scale: 1, y: 0 }}
            exit={{ scale: 0.96, y: 4 }}
            transition={{ duration: 0.12 }}
            onClick={(e) => e.stopPropagation()}
            className="w-full max-w-md rounded-md border border-red-500/40 bg-[var(--bg-elevated)] p-5"
          >
            <h3 className="text-base font-semibold">Restore from this backup?</h3>
            <p className="mt-2 text-xs text-[var(--text-secondary)]">
              kuso starts a one-shot Job that pipes{" "}
              <span className="font-mono">{tail(item.key)}</span> into the live database via
              <span className="font-mono"> psql</span>. Existing tables will be overwritten in
              place — there is no rollback.
            </p>
            <div className="mt-4 flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={onCancel} disabled={pending}>
                Cancel
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => onConfirm(item.key)}
                disabled={pending}
              >
                <RotateCcw className="h-3 w-3" />
                {pending ? "Starting…" : "Run restore"}
              </Button>
            </div>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}

function SQLTab({ project, addon }: { project: string; addon: string }) {
  const tables = useQuery({
    queryKey: ["addons", project, addon, "sql", "tables"],
    queryFn: () => listSQLTables(project, addon),
    staleTime: 30_000,
    // Retry transient 502/504s while the postgres pod is still
    // booting. Without this the first failed fetch sticks around
    // forever even after the addon comes up.
    retry: 2,
    retryDelay: (i) => 1000 * (i + 1),
  });
  const [query, setQuery] = useState("SELECT 1");
  const [resp, setResp] = useState<{
    columns: string[];
    rows: string[][];
    truncated: boolean;
    elapsed: string;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = useMutation({
    mutationFn: (q: string) => runSQL(project, addon, q, 100),
    onSuccess: (data) => {
      setResp(data);
      setError(null);
    },
    onError: (e) => {
      setError(e instanceof Error ? e.message : "query failed");
      setResp(null);
    },
  });

  const pickTable = (schema: string, name: string) => {
    const safe =
      schema === "public" ? `"${name}"` : `"${schema}"."${name}"`;
    setQuery(`SELECT * FROM ${safe} LIMIT 100`);
  };

  return (
    <div className="grid h-full grid-cols-[200px_1fr] gap-0">
      <aside className="overflow-y-auto border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3">
        <h4 className="mb-2 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          tables
        </h4>
        {tables.isPending ? (
          <Skeleton className="h-32 w-full" />
        ) : tables.isError ? (
          <div className="space-y-2 text-[10px]">
            <p className="text-amber-400">
              {tables.error instanceof Error ? tables.error.message : "load failed"}
            </p>
            <p className="text-[var(--text-tertiary)]">
              Postgres may still be starting. Click to retry.
            </p>
            <button
              type="button"
              onClick={() => tables.refetch()}
              className="rounded border border-[var(--border-subtle)] px-2 py-1 font-mono text-[10px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]"
            >
              Retry
            </button>
          </div>
        ) : (tables.data ?? []).length === 0 ? (
          <p className="text-[10px] text-[var(--text-tertiary)]">no user tables</p>
        ) : (
          <ul className="space-y-0.5">
            {tables.data!.map((t) => (
              <li key={`${t.schema}.${t.name}`}>
                <button
                  type="button"
                  onClick={() => pickTable(t.schema, t.name)}
                  className="block w-full truncate rounded px-2 py-1 text-left font-mono text-[11px] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                  title={`${t.schema}.${t.name}`}
                >
                  {t.schema === "public" ? t.name : `${t.schema}.${t.name}`}
                </button>
              </li>
            ))}
          </ul>
        )}
      </aside>

      <section className="flex min-h-0 flex-col">
        <div className="border-b border-[var(--border-subtle)] p-3">
          <textarea
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            spellCheck={false}
            rows={4}
            placeholder="SELECT * FROM users LIMIT 100"
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
            onKeyDown={(e) => {
              // ⌘/Ctrl+Enter runs the query — same shortcut every other
              // SQL UI uses, so muscle memory carries over from psql,
              // DBeaver, etc.
              if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                e.preventDefault();
                run.mutate(query);
              }
            }}
          />
          <div className="mt-2 flex items-center justify-between">
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              read-only · 5s timeout · max 100 rows · ⌘/Ctrl + ↵ to run
            </span>
            <Button size="sm" onClick={() => run.mutate(query)} disabled={run.isPending}>
              <Play className="h-3 w-3" />
              {run.isPending ? "Running…" : "Run"}
            </Button>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-3">
          {error ? (
            <pre className="whitespace-pre-wrap rounded-md border border-red-500/30 bg-red-500/5 p-3 font-mono text-[11px] text-red-400">
              {error}
            </pre>
          ) : resp ? (
            <SQLResults resp={resp} />
          ) : (
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              no results yet — write a query and hit Run.
            </p>
          )}
        </div>
      </section>
    </div>
  );
}

function SQLResults({
  resp,
}: {
  resp: { columns: string[]; rows: string[][]; truncated: boolean; elapsed: string };
}) {
  if (resp.columns.length === 0) {
    return (
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
        query returned no columns ({resp.elapsed})
      </p>
    );
  }
  return (
    <div className="overflow-auto rounded-md border border-[var(--border-subtle)]">
      <table className="w-full text-left font-mono text-[11px]">
        <thead className="bg-[var(--bg-secondary)] text-[var(--text-tertiary)]">
          <tr>
            {resp.columns.map((c) => (
              <th key={c} className="border-b border-[var(--border-subtle)] px-2 py-1.5 font-medium">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {resp.rows.map((row, i) => (
            <tr key={i} className="border-b border-[var(--border-subtle)] last:border-b-0 hover:bg-[var(--bg-tertiary)]/30">
              {row.map((cell, j) => (
                <td key={j} className="px-2 py-1 align-top text-[var(--text-secondary)]">
                  {cell === "" ? (
                    <span className="text-[var(--text-tertiary)]/60 italic">null</span>
                  ) : cell.length > 200 ? (
                    cell.slice(0, 200) + "…"
                  ) : (
                    cell
                  )}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <footer className="flex items-center justify-between border-t border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2 py-1 font-mono text-[10px] text-[var(--text-tertiary)]">
        <span>
          {resp.rows.length} row{resp.rows.length === 1 ? "" : "s"}
          {resp.truncated && (
            <span className="ml-2 text-amber-400">· truncated at 100</span>
          )}
        </span>
        <span>{resp.elapsed}</span>
      </footer>
    </div>
  );
}

// PlacementSection edits spec.placement on a KusoAddon. Mirrors the
// shape of ServiceSettingsPanel's PlacementSection: a header strip
// with a hint badge, native selects driven by what the cluster
// actually carries, a pill-list of specific hostnames, and a live
// match preview. Server validates the selector matches ≥1 node at
// save time and 400s with "no cluster node matches placement" if not.
function PlacementSection({
  project,
  addon,
  cr,
}: {
  project: string;
  addon: string;
  cr?: import("@/types/projects").KusoAddon;
}) {
  const qc = useQueryClient();
  const initialLabels: Record<string, string> = (cr?.spec as { placement?: { labels?: Record<string, string> } })?.placement?.labels ?? {};
  const initialNodes: string[] = (cr?.spec as { placement?: { nodes?: string[] } })?.placement?.nodes ?? [];

  const nodesQuery = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    staleTime: 30_000,
  });

  const [labels, setLabels] = useState<{ key: string; value: string }[]>(
    Object.entries(initialLabels).map(([k, v]) => ({ key: k, value: v }))
  );
  const [pickedNodes, setPickedNodes] = useState<string[]>(initialNodes);

  // Re-baseline when the CR changes (after a save lands).
  useEffect(() => {
    setLabels(Object.entries(initialLabels).map(([k, v]) => ({ key: k, value: v })));
    setPickedNodes(initialNodes);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(initialLabels), JSON.stringify(initialNodes)]);

  // Catalog of label keys + values that real cluster nodes carry, so
  // the rule editor offers them as native selects (no typo path).
  const allLabels = new Map<string, Set<string>>();
  for (const n of nodesQuery.data ?? []) {
    for (const [k, v] of Object.entries(n.kusoLabels ?? {})) {
      if (!allLabels.has(k)) allLabels.set(k, new Set());
      allLabels.get(k)!.add(v);
    }
  }
  const allHostnames = (nodesQuery.data ?? []).map((n) => n.name);

  // Live match preview — same logic as the service variant. Empty rules
  // schedule everywhere; a partial rule (key set but no value) doesn't
  // skew the count.
  const matching = (nodesQuery.data ?? []).filter((n) => {
    for (const r of labels) {
      if (!r.key.trim()) continue;
      if ((n.kusoLabels ?? {})[r.key.trim()] !== r.value) return false;
    }
    if (pickedNodes.length > 0 && !pickedNodes.includes(n.name)) return false;
    return true;
  });
  const totalNodes = (nodesQuery.data ?? []).length;
  const incompleteRules = labels.filter((r) => !r.key.trim() || !r.value.trim()).length;
  const hasEffectiveRules =
    labels.some((r) => r.key.trim() && r.value.trim()) || pickedNodes.length > 0;

  const save = useMutation({
    mutationFn: () => {
      const lbls: Record<string, string> = {};
      for (const r of labels) {
        if (r.key.trim() && r.value.trim()) lbls[r.key.trim()] = r.value.trim();
      }
      return setAddonPlacement(project, addon, { labels: lbls, nodes: pickedNodes });
    },
    onSuccess: () => {
      toast.success("Placement saved");
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  const dirty =
    JSON.stringify(
      Object.fromEntries(
        labels.filter((r) => r.key.trim() && r.value.trim()).map((r) => [r.key.trim(), r.value.trim()])
      )
    ) !== JSON.stringify(initialLabels) ||
    JSON.stringify(pickedNodes) !== JSON.stringify(initialNodes);

  const addLabel = () =>
    setLabels((cur) => [...cur, { key: "", value: "" }]);
  const updLabel = (i: number, patch: Partial<{ key: string; value: string }>) =>
    setLabels((cur) => cur.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const rmLabel = (i: number) =>
    setLabels((cur) => cur.filter((_, j) => j !== i));
  const toggleNode = (name: string) =>
    setPickedNodes((cur) =>
      cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]
    );

  return (
    <section className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <div className="flex items-center gap-2">
          <MapPin className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          <h4 className="text-sm font-semibold">Placement</h4>
        </div>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {!hasEffectiveRules
            ? "schedules anywhere"
            : incompleteRules > 0
              ? `${incompleteRules} incomplete`
              : `${matching.length}/${totalNodes} match`}
        </span>
      </header>
      <p className="border-b border-[var(--border-subtle)] px-3 py-2 text-[11px] text-[var(--text-secondary)]">
        Pin this addon to a subset of cluster nodes. Use{" "}
        <span className="font-mono text-[var(--text-tertiary)]">key=value</span> rules
        (e.g. <span className="font-mono">region=eu</span> or{" "}
        <span className="font-mono">tier=db</span>) to match nodes by label, or pick
        specific hostnames below. Empty rules schedule anywhere.
      </p>
      {labels.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">No label rules.</p>
      ) : (
        labels.map((r, i) => {
          const valuesForKey = allLabels.get(r.key.trim());
          const haveAnyLabels = allLabels.size > 0;
          return (
            <div
              key={i}
              className="grid grid-cols-[140px_1fr_28px] items-center gap-1.5 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
            >
              {haveAnyLabels ? (
                <select
                  value={r.key}
                  onChange={(e) => updLabel(i, { key: e.target.value, value: "" })}
                  className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
                >
                  <option value="">— pick label key —</option>
                  {[...allLabels.keys()].sort().map((k) => (
                    <option key={k} value={k}>
                      {k}
                    </option>
                  ))}
                </select>
              ) : (
                <Input
                  value={r.key}
                  onChange={(e) => updLabel(i, { key: e.target.value })}
                  placeholder="region"
                  className="h-7 font-mono text-[11px]"
                />
              )}
              {valuesForKey && valuesForKey.size > 0 ? (
                <select
                  value={r.value}
                  onChange={(e) => updLabel(i, { value: e.target.value })}
                  className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
                >
                  <option value="">— pick value —</option>
                  {[...valuesForKey].sort().map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </select>
              ) : (
                <Input
                  value={r.value}
                  onChange={(e) => updLabel(i, { value: e.target.value })}
                  placeholder={r.key.trim() ? "value" : "eu"}
                  className="h-7 font-mono text-[11px]"
                  disabled={haveAnyLabels && !r.key.trim()}
                />
              )}
              <button
                type="button"
                onClick={() => rmLabel(i)}
                aria-label="Remove rule"
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
              >
                <X className="h-3 w-3" />
              </button>
            </div>
          );
        })
      )}
      <button
        type="button"
        onClick={addLabel}
        className="flex w-full items-center gap-1.5 border-y border-[var(--border-subtle)] px-3 py-2 text-left text-[11px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Plus className="h-3 w-3" />
        add label rule
      </button>

      <div className="px-3 py-2">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-[12px] text-[var(--text-secondary)]">specific nodes</span>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {pickedNodes.length === 0
              ? "any node matching the labels"
              : `${pickedNodes.length} pinned`}
          </span>
        </div>
        {allHostnames.length === 0 ? (
          <p className="text-[11px] text-[var(--text-tertiary)]">No nodes visible yet.</p>
        ) : (
          <div className="flex flex-wrap gap-1">
            {allHostnames.map((h) => {
              const picked = pickedNodes.includes(h);
              return (
                <button
                  key={h}
                  type="button"
                  onClick={() => toggleNode(h)}
                  className={cn(
                    "inline-flex h-6 items-center rounded-md border px-2 font-mono text-[10px] transition-colors",
                    picked
                      ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
                  )}
                >
                  {h}
                </button>
              );
            })}
          </div>
        )}
      </div>

      {hasEffectiveRules && (
        <div className="border-t border-[var(--border-subtle)] px-3 py-2 text-[10px] text-[var(--text-tertiary)]">
          {incompleteRules > 0 ? (
            <span>
              fill in the {incompleteRules === 1 ? "empty rule" : "empty rules"} or remove{" "}
              {incompleteRules === 1 ? "it" : "them"} to see what matches
            </span>
          ) : (
            <span>
              {matching.length}/{totalNodes} cluster nodes match this placement
            </span>
          )}
        </div>
      )}

      <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-3 py-2">
        <Button
          size="sm"
          variant="ghost"
          disabled={!dirty || save.isPending}
          onClick={() => {
            setLabels(Object.entries(initialLabels).map(([k, v]) => ({ key: k, value: v })));
            setPickedNodes(initialNodes);
          }}
        >
          Reset
        </Button>
        <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? "Saving…" : "Save placement"}
        </Button>
      </footer>
    </section>
  );
}

function SettingsTab({
  project,
  addon,
  cr,
  onClose,
}: {
  project: string;
  addon: string;
  cr?: import("@/types/projects").KusoAddon;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [text, setText] = useState("");
  const del = useMutation({
    mutationFn: () => deleteAddon(project, addon),
    onSuccess: () => {
      toast.success(`Addon ${addon} deleted`);
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  return (
    <div className="space-y-4 p-5">
      <PlacementSection project={project} addon={addon} cr={cr} />
      <section className="rounded-md border border-red-500/30 bg-red-500/5 p-4">
        <h4 className="text-sm font-semibold">Delete addon</h4>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Removes the addon and tears down the Helm release. The PVC + data go with it
          unless your storage class retains it. There is no undo.
        </p>
        {!confirming ? (
          <Button
            variant="outline"
            size="sm"
            className="mt-3"
            onClick={() => setConfirming(true)}
          >
            <Trash2 className="h-3.5 w-3.5" /> Delete addon
          </Button>
        ) : (
          <div className="mt-3 space-y-2">
            <p className="text-xs">
              Type <span className="font-mono">{addon}</span> to confirm.
            </p>
            <Input
              value={text}
              onChange={(e) => setText(e.target.value)}
              className="font-mono text-sm"
              autoFocus
            />
            <div className="flex gap-2">
              <Button
                variant="destructive"
                size="sm"
                disabled={text !== addon || del.isPending}
                onClick={() => del.mutate()}
              >
                {del.isPending ? "Deleting…" : "Confirm delete"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setConfirming(false);
                  setText("");
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

// ---------- helpers ----------

function tail(key: string): string {
  const i = key.lastIndexOf("/");
  return i >= 0 ? key.slice(i + 1) : key;
}

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v.toFixed(v >= 100 ? 0 : 1) + " " + units[i];
}
