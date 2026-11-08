"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, Eye, EyeOff, Check } from "lucide-react";
import { useServices } from "@/features/projects";
import {
  getAddonConnection,
  revealAddonSecret,
  useAddonHealth,
  useSubscribedAddonsForServices,
  useToggleAddonSubscription,
  useAddonBackups,
  backupSummary,
  backupsApplicable,
  backupsListState,
  type AddonConnection,
} from "@/features/addons";
import { copyText } from "@/lib/clipboard";
import { StatusDot } from "@/components/ui/status-pill";
import { Button } from "@/components/ui/button";
import { useCanOnProject, Perms } from "@/features/auth";
import { cn, serviceShortName } from "@/lib/utils";
import { resourceGroup } from "@/lib/env-group";
import { toast } from "sonner";
import { LoadingState } from "@/components/ui/loading-state";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { AddonPodsSection } from "./AddonPodsSection";
import { CopyButton } from "@/components/ui/copy-button";

// storageSizeFromSpec mirrors the helm chart's kusoaddon.storageSize
// helper. spec.storageSize (explicit) wins over the t-shirt size mapping.
function storageSizeFromSpec(
  spec: { storageSize?: string; size?: "small" | "medium" | "large" } | undefined,
): string {
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

export function OverviewTab({
  project,
  addon,
  kind,
  cr,
}: {
  project: string;
  addon: string;
  kind: string;
  cr?: import("@/types/projects").KusoAddon;
}) {
  const canReadSecrets = useCanOnProject(project, Perms.SecretsRead);
  // The connection secret is provisioned async by the operator; it can
  // take a few seconds after the addon is created. Refetch slowly when
  // it isn't ready yet so the panel auto-fills.
  const conn = useQuery({
    queryKey: ["addons", project, addon, "connection"],
    queryFn: () => getAddonConnection(project, addon),
    enabled: canReadSecrets,
    // An empty {values:{}} is "not generated yet" too: keep polling.
    refetchInterval: (q) => (Object.keys(q.state.data?.values ?? {}).length > 0 ? false : 5_000),
  });
  const storageSize = storageSizeFromSpec(cr?.spec);
  const tier = cr?.spec.size ?? "small";
  return (
    <div className="space-y-4 p-5">
      <HealthStrip project={project} addon={addon} kind={kind} cr={cr} storageSize={storageSize} tier={tier} />

      <UsedBy
        project={project}
        addon={serviceShortName(project, addon)}
        group={cr ? resourceGroup(cr.metadata.labels) : undefined}
      />

      {/* External addons render no workload unless a pooler fronts them. */}
      {(!cr?.spec.external || cr.spec.pooler?.enabled) && (
        <AddonPodsSection project={project} addon={addon} />
      )}

      <section
        id="addon-connection"
        aria-labelledby="addon-connection-title"
        className="scroll-mt-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
      >
        <header className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
          <h3 id="addon-connection-title" className="text-sm font-medium text-[var(--text-primary)]">Connection</h3>
          <PublicAccessBadge publicTCP={cr?.spec.publicTCP} />
        </header>
        {!canReadSecrets ? (
          <p className="px-3 py-3 text-xs text-[var(--text-secondary)]">
            Only project admins can view connection details.
          </p>
        ) : conn.isPending ? (
          <LoadingState kind="inline" className="px-3 py-3" />
        ) : conn.isError ? (
          <p className="px-3 py-3 text-xs text-[var(--warning)]">
            {conn.error instanceof Error ? conn.error.message : "load failed"}
          </p>
        ) : Object.keys(conn.data?.values ?? {}).length === 0 ? (
          <p className="px-3 py-3 text-xs text-[var(--text-secondary)]">
            Connection details are still being generated…
          </p>
        ) : (
          <ConnectionRows project={project} addon={addon} conn={conn.data!} publicTCP={cr?.spec.publicTCP} />
        )}
      </section>
    </div>
  );
}

// UsedBy lists the project's services as chips; a filled chip means the
// service gets this addon's connection env vars. Editors toggle in place.
// Only services in the addon's own env-group are listed: connecting a
// staging clone to the production DB (or vice versa) crosses groups.
// `group` is undefined until the addon CR loads.
function UsedBy({ project, addon, group }: { project: string; addon: string; group: string | undefined }) {
  const services = useServices(project);
  const names =
    group === undefined
      ? []
      : (services.data ?? [])
          .filter((s) => resourceGroup(s.metadata.labels) === group)
          .map((s) => serviceShortName(project, s.metadata.name));
  const subs = useSubscribedAddonsForServices(project, names);
  const toggle = useToggleAddonSubscription(project);
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const [pending, setPending] = useState<string | null>(null);
  // Connecting/disconnecting rewrites the service's env and restarts its
  // pods (disconnect drops DATABASE_URL & co.), so it gets a confirm.
  const [confirm, setConfirm] = useState<{ svc: string; on: boolean } | null>(null);
  const apply = (svc: string, on: boolean) => {
    const q = subs[names.indexOf(svc)];
    if (!q?.data) return;
    setPending(svc);
    toggle.mutate(
      { service: svc, addon, on, current: q.data },
      {
        onSuccess: () => toast.success(on ? `Connected ${svc} to ${addon}` : `Disconnected ${svc} from ${addon}`),
        onError: (e) => toast.error(e instanceof Error ? e.message : "Update failed"),
        onSettled: () => {
          setPending(null);
          setConfirm(null);
        },
      },
    );
  };

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <div className="flex items-start justify-between gap-3 px-3 py-2">
        <span className="pt-1 text-sm font-medium text-[var(--text-primary)]">Used by</span>
        {services.isPending || group === undefined ? (
          <LoadingState kind="inline" />
        ) : names.length === 0 ? (
          <span className="text-xs text-[var(--text-secondary)]">No services in this environment yet</span>
        ) : (
          <div className="flex flex-wrap justify-end gap-1">
            {names.map((svc, i) => {
              const q = subs[i];
              const on = !!q?.data?.subscribed.includes(addon);
              const busy = pending === svc || !q?.data;
              return (
                <button
                  key={svc}
                  type="button"
                  aria-pressed={on}
                  disabled={!canWrite || busy}
                  title={canWrite ? (on ? "Disconnect" : "Connect") : undefined}
                  onClick={() => {
                    if (!q?.data) return;
                    setConfirm({ svc, on: !on });
                  }}
                  className={cn(
                    "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 font-mono text-xs transition-colors",
                    on
                      ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] text-[var(--text-tertiary)]",
                    canWrite && !busy && "hover:border-[var(--accent)]/60",
                    busy && "opacity-60",
                  )}
                >
                  {on && <Check className="h-3 w-3" />}
                  {svc}
                </button>
              );
            })}
          </div>
        )}
      </div>
      <ConfirmDialog
        open={confirm !== null}
        title={confirm?.on ? `Connect ${confirm.svc} to ${addon}?` : `Disconnect ${confirm?.svc ?? ""} from ${addon}?`}
        body={
          confirm?.on
            ? `${confirm.svc} gets ${addon}'s connection variables and its pods restart to pick them up.`
            : `${confirm?.svc ?? ""} loses ${addon}'s connection variables (e.g. DATABASE_URL) and its pods restart without them.`
        }
        confirmLabel={confirm?.on ? "Connect & restart" : "Disconnect & restart"}
        destructive={!confirm?.on}
        pending={pending !== null}
        onCancel={() => setConfirm(null)}
        onConfirm={() => confirm && apply(confirm.svc, confirm.on)}
      />
    </section>
  );
}

// ConnectionRows renders the addon's connection info: plain values (host,
// port, user, database) in the clear; credentials masked until the user
// reveals or copies one. Each reveal/copy fetches that single value, and
// the server audits it per key — merely opening the overview no longer
// discloses (or logs) anything.
//
// Sort: URL-style keys first, password last, the rest alphabetical.
// When publicTCP is on, one masked PUBLIC_URL row (the primary DSN
// pointed at <cluster-host>:<public-port>) sits on top.
function ConnectionRows({
  project,
  addon,
  conn,
  publicTCP,
}: {
  project: string;
  addon: string;
  conn: AddonConnection;
  publicTCP?: { enabled?: boolean; port?: number };
}) {
  const { values, masked } = conn;
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const [shown, setShown] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const keys = Object.keys(values).sort((a, b) => {
    const score = (k: string) => (/url$|uri$/i.test(k) ? 0 : /password|secret|token/i.test(k) ? 2 : 1);
    const sa = score(a);
    const sb = score(b);
    return sa !== sb ? sa - sb : a.localeCompare(b);
  });
  const isMasked = (k: string) => masked.includes(k);

  // plaintext returns a key's real value, fetching (and so auditing) it
  // the first time when the server withheld it.
  const plaintext = async (k: string): Promise<string> => {
    if (!isMasked(k)) return values[k] ?? "";
    if (conn.legacyPlain) return conn.legacyPlain[k] ?? "";
    if (k in revealed) return revealed[k];
    setBusy(k);
    try {
      const v = await revealAddonSecret(project, addon, k);
      setRevealed((r) => ({ ...r, [k]: v }));
      return v;
    } finally {
      setBusy((b) => (b === k ? null : b));
    }
  };

  const publicEnabled = !!publicTCP?.enabled && typeof publicTCP?.port === "number" && publicTCP.port > 0;
  const publicHost = typeof window !== "undefined" ? window.location.hostname : "<your-cluster-host>";
  const publicSource = ["DATABASE_URL", "DIRECT_URL", "POOLER_URL", "REDIS_URL"].find((k) => (values[k] ?? "") !== "");
  const toPublic = (dsn: string): string | null => {
    try {
      const u = new URL(dsn);
      u.host = `${publicHost}:${publicTCP!.port}`;
      return u.toString();
    } catch {
      return null;
    }
  };

  // The chart always emits POOLER_* keys; with the pooler off they are
  // empty, which read as broken values. Fold them into one explanatory row.
  const poolerKeys = keys.filter((k) => /^POOLER_/.test(k));
  const poolerOff = poolerKeys.length > 0 && poolerKeys.every((k) => (values[k] ?? "").trim() === "");
  const shownKeys = poolerOff ? keys.filter((k) => !poolerKeys.includes(k)) : keys;

  const reveal = async (k: string, source = k, transform?: (v: string) => string | null) => {
    if (shown[k]) {
      setShown((s) => ({ ...s, [k]: false }));
      return;
    }
    try {
      const v = await plaintext(source);
      if (transform && transform(v) === null) throw new Error("Couldn't build the public URL");
      setShown((s) => ({ ...s, [k]: true }));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Couldn't reveal the value");
    }
  };
  const copy = async (k: string, source = k, transform?: (v: string) => string | null) => {
    try {
      const raw = await plaintext(source);
      const v = transform ? transform(raw) : raw;
      if (v === null) throw new Error("Couldn't build the public URL");
      if (!(await copyText(v))) throw new Error("Clipboard unavailable");
      toast.success(`Copied ${k}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Copy failed");
    }
  };

  const rows: { k: string; source: string; transform?: (v: string) => string | null; accent?: boolean }[] = [];
  if (publicEnabled && publicSource) rows.push({ k: "PUBLIC_URL", source: publicSource, transform: toPublic, accent: true });
  for (const k of shownKeys) rows.push({ k, source: k });

  return (
    <ul>
      {rows.map((r, i) => {
        const secret = isMasked(r.source);
        const raw = !secret ? (values[r.source] ?? "") : revealed[r.source] ?? conn.legacyPlain?.[r.source];
        const value = raw !== undefined && r.transform ? (r.transform(raw) ?? "") : raw;
        return (
          <ConnectionRow
            key={r.k}
            k={r.k}
            preview={secret ? (r.transform ? "" : values[r.source] ?? "") : (values[r.source] ?? "")}
            value={value}
            secret={secret}
            empty={!secret && (values[r.source] ?? "").trim() === ""}
            visible={!secret || !!shown[r.k]}
            busy={busy === r.source}
            accent={r.accent}
            last={i === rows.length - 1 && !poolerOff}
            onToggle={() => void reveal(r.k, r.source, r.transform)}
            onCopy={() => void copy(r.k, r.source, r.transform)}
          />
        );
      })}
      {poolerOff && (
        <li className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)] items-center gap-2 px-3 py-2 sm:grid-cols-[180px_minmax(0,1fr)]">
          <span className="font-mono text-xs text-[var(--text-secondary)]">POOLER_*</span>
          <span className="text-xs text-[var(--text-tertiary)]">
            Connection pooler not enabled. Turn on PgBouncer in Settings to get pooled URLs.
          </span>
        </li>
      )}
    </ul>
  );
}

function ConnectionRow({
  k,
  preview,
  value,
  secret,
  empty,
  visible,
  busy,
  accent,
  last,
  onToggle,
  onCopy,
}: {
  k: string;
  // What's shown while masked: a server-redacted DSN, or "" for dots.
  preview: string;
  value: string | undefined;
  secret: boolean;
  empty: boolean;
  visible: boolean;
  busy: boolean;
  accent?: boolean;
  last?: boolean;
  onToggle: () => void;
  onCopy: () => void;
}) {
  return (
    <li className={cn("px-3 py-2", !last && "border-b border-[var(--border-subtle)]", accent && "bg-[var(--warning-subtle)]")}>
      <div className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)_auto] items-center gap-2 sm:grid-cols-[180px_minmax(0,1fr)_auto]">
        <span
          className={cn("truncate font-mono text-xs", accent ? "text-[var(--warning)]" : "text-[var(--text-secondary)]")}
          title={k}
        >
          {k}
        </span>
        {empty ? (
          <span className="truncate px-2 py-1 text-xs italic text-[var(--text-tertiary)]">Not set</span>
        ) : (
          <span
            className={cn(
              "truncate rounded-md bg-[var(--bg-primary)] px-2 py-1 font-mono text-xs",
              visible ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]",
            )}
            title={visible ? value : "Hidden. Reveal or copy it; both are recorded in the audit log."}
          >
            {visible && value !== undefined ? value : preview || "••••••••••••••••"}
          </span>
        )}
        <div className={cn("flex items-center gap-0.5", empty && "invisible")}>
          {secret && (
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={visible ? `Hide ${k}` : `Reveal ${k}`}
              title={visible ? "Hide" : "Reveal (recorded in the audit log)"}
              disabled={busy}
              onClick={onToggle}
            >
              {visible ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={`Copy ${k}`}
            title={secret ? "Copy (recorded in the audit log)" : "Copy"}
            disabled={busy}
            onClick={onCopy}
          >
            <Copy className="h-3 w-3" />
          </Button>
        </div>
      </div>
    </li>
  );
}

// PublicAccessBadge surfaces the addon's public-TCP state in the
// Connection card header so the user sees at a glance whether the
// addon is internal-only or also publicly reachable. Hidden when no
// publicTCP block is present (the default — no extra noise on the
// common-case internal-only addon).
function PublicAccessBadge({
  publicTCP,
}: {
  publicTCP?: { enabled?: boolean; port?: number };
}) {
  if (!publicTCP) return null;
  const enabled =
    !!publicTCP.enabled && typeof publicTCP.port === "number" && publicTCP.port > 0;
  if (!enabled) {
    return (
      <span className="rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 py-0.5 text-xs text-[var(--text-secondary)]">
        Internal only
      </span>
    );
  }
  return (
    <span className="rounded border border-[var(--warning)]/40 bg-[var(--warning-subtle)] px-1.5 py-0.5 text-xs text-[var(--warning)]">
      Public · port {publicTCP.port}
    </span>
  );
}

// HealthStrip is the at-a-glance row: is it up, what version, how big,
// and is it backed up.
function HealthStrip({
  project,
  addon,
  kind,
  cr,
  storageSize,
  tier,
}: {
  project: string;
  addon: string;
  kind: string;
  cr?: import("@/types/projects").KusoAddon;
  storageSize: string;
  tier: string;
}) {
  const health = useAddonHealth(project, addon, cr);
  const hasBackups = backupsApplicable(cr);
  const list = useAddonBackups(project, addon, hasBackups);
  const summary = hasBackups ? backupSummary(cr, backupsListState(list)) : null;
  const tiles: { k: string; v: React.ReactNode; d?: React.ReactNode }[] = [
    {
      k: "Status",
      v: (
        <span className="inline-flex items-center gap-1.5">
          <StatusDot status={health.status} />
          {health.status.label}
        </span>
      ),
      d: health.detail,
    },
    { k: "Version", v: cr?.spec.version || "Chart default", d: kind },
    {
      k: "Size",
      v: <span className="capitalize">{tier}</span>,
      d: `${storageSize} disk${cr?.spec.ha ? " · HA" : ""}`,
    },
  ];
  return (
    <section aria-label="Addon health" className="grid grid-cols-3 gap-px overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--border-subtle)]">
      {tiles.map((t) => (
        <div key={t.k} className="min-w-0 bg-[var(--bg-secondary)] px-3 py-2.5">
          <div className="text-xs text-[var(--text-secondary)]">{t.k}</div>
          <div className="mt-0.5 truncate text-sm font-medium text-[var(--text-primary)]">{t.v}</div>
          {t.d && <div className="mt-0.5 truncate text-xs text-[var(--text-tertiary)]">{t.d}</div>}
        </div>
      ))}
      {/* Full width so the whole sentence fits: it's the same backupSummary
          wording the Backups tab shows. */}
      <div className="col-span-3 flex flex-wrap items-baseline gap-x-2 gap-y-0.5 bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
        <span>Backups</span>
        {summary ? (
          <span>
            <span className="text-sm font-medium text-[var(--text-primary)]">{summary.headline}</span>
            <span className="text-[var(--text-tertiary)]">{summary.sentence.slice(summary.headline.length)}</span>
          </span>
        ) : (
          <span className="text-[var(--text-tertiary)]">Not available for {kind || "this kind"} addons</span>
        )}
      </div>
      <div className="col-span-3 flex items-center gap-2 bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)]">
        <span>Name</span>
        <span className="font-mono text-[var(--text-primary)]">{addon}</span>
        <CopyButton value={addon} label="Copy addon name" size="xs" />
      </div>
    </section>
  );
}
