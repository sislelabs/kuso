"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, Eye, EyeOff, Check } from "lucide-react";
import { addonSecret, useServices } from "@/features/projects";
import { useSubscribedAddonsForServices, useToggleAddonSubscription } from "@/features/addons";
import { Button } from "@/components/ui/button";
import { useCanOnProject, Perms } from "@/features/auth";
import { cn, serviceShortName } from "@/lib/utils";
import { resourceGroup } from "@/lib/env-group";
import { toast } from "sonner";
import { LoadingState } from "@/components/ui/loading-state";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { AddonPodsSection } from "./AddonPodsSection";
import { useAddonPods } from "@/features/pods";
import { CopyButton } from "@/components/shared/s2-CopyButton";

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
    queryKey: ["addons", project, addon, "secret"],
    queryFn: () => addonSecret(project, addon),
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

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
          <h3 className="text-sm font-medium text-[var(--text-primary)]">Connection</h3>
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
          <ConnectionRows values={conn.data!.values} publicTCP={cr?.spec.publicTCP} />
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
                    "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 font-mono text-[11px] transition-colors",
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

// ConnectionRows renders the addon's connection secret as a list of
// key/value rows with copy + show/hide. Sensitive values (password,
// secret-looking keys, the URL) are masked by default; the user has to
// reveal explicitly. Sort: URL-style keys first, password last, the
// rest alphabetical — matches what people scan for when they want to
// connect.
//
// When the addon's publicTCP is enabled, a SINGLE masked "public URL" row is
// rendered at the top of the list (the credentialed DSN rewritten to
// <cluster-host>:<public-port>). It's masked + copy-only like the password —
// never shown in plaintext — instead of repeating a verbose "also reachable
// publicly at postgres://user:password@…" note under every host/port/url row.
function ConnectionRows({
  values,
  publicTCP,
}: {
  values: Record<string, string>;
  publicTCP?: { enabled?: boolean; port?: number };
}) {
  const [shown, setShown] = useState<Record<string, boolean>>({});
  const [copied, setCopied] = useState<string | null>(null);
  const keys = Object.keys(values).sort((a, b) => {
    const score = (k: string) =>
      /url$/i.test(k) ? 0 : /password|secret|token/i.test(k) ? 2 : 1;
    const sa = score(a);
    const sb = score(b);
    return sa !== sb ? sa - sb : a.localeCompare(b);
  });
  // Mask by default; only plainly non-secret keys show in the clear. A
  // denylist missed MONGODB_URI, MEILI_MASTER_KEY and external *_DSN/*_KEY.
  const isSensitive = (k: string) =>
    !/(^|_)(HOST|HOSTNAME|PORT|DATABASE|DB|USER|USERNAME|REGION|BUCKET|SSLMODE)$/i.test(k) ||
    /:\/\/[^/\s]*@/.test(values[k] ?? "");

  // The single public DSN: rewrite the primary connection URL's host:port →
  // <cluster-host>:<public-port>, keeping scheme / user:password / path / query.
  const publicEnabled =
    !!publicTCP?.enabled && typeof publicTCP?.port === "number" && publicTCP.port > 0;
  const publicHost =
    typeof window !== "undefined" ? window.location.hostname : "<your-cluster-host>";
  const publicUrl = (() => {
    if (!publicEnabled) return null;
    const src =
      values.DATABASE_URL || values.DIRECT_URL || values.POOLER_URL || values.REDIS_URL || "";
    if (!src) return null;
    try {
      const u = new URL(src);
      u.host = `${publicHost}:${publicTCP!.port}`;
      return u.toString();
    } catch {
      return null;
    }
  })();

  // The chart always emits POOLER_* keys; with the pooler off they are
  // empty, which read as broken values. Fold them into one explanatory row.
  const poolerKeys = keys.filter((k) => /^POOLER_/.test(k));
  const poolerOff = poolerKeys.length > 0 && poolerKeys.every((k) => (values[k] ?? "").trim() === "");
  const shownKeys = poolerOff ? keys.filter((k) => !poolerKeys.includes(k)) : keys;

  const onCopy = async (k: string, v: string) => {
    try {
      await navigator.clipboard.writeText(v);
      setCopied(k);
      setTimeout(() => setCopied((c) => (c === k ? null : c)), 1200);
    } catch {
      toast.error("clipboard unavailable");
    }
  };

  // A field row: monospace key, masked-or-revealed value, optional eye, copy.
  const FieldRow = ({
    k,
    v,
    sensitive,
    last,
    accent,
  }: {
    k: string;
    v: string;
    sensitive: boolean;
    last?: boolean;
    accent?: boolean;
  }) => {
    const empty = v.trim() === "";
    const visible = !sensitive || !!shown[k];
    return (
      <li
        className={cn(
          "px-3 py-2",
          !last && "border-b border-[var(--border-subtle)]",
          accent && "bg-[var(--warning-subtle)]",
        )}
      >
        <div className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)_auto] items-center gap-2 sm:grid-cols-[180px_minmax(0,1fr)_auto]">
          <span
            className={cn("truncate font-mono text-xs", accent ? "text-[var(--warning)]" : "text-[var(--text-secondary)]")}
            title={k}
          >
            {k}
          </span>
          {empty ? (
            <span className="truncate px-2 py-1 text-xs italic text-[var(--text-tertiary)]">
              Not set
            </span>
          ) : (
            <span
              className={cn(
                "truncate rounded-md bg-[var(--bg-primary)] px-2 py-1 font-mono text-xs",
                visible ? "text-[var(--text-primary)]" : "tracking-widest text-[var(--text-tertiary)]",
              )}
              title={visible ? v : "Hidden. Use the eye button to reveal."}
            >
              {visible ? v : "•".repeat(Math.min(v.length, 24))}
            </span>
          )}
          <div className={cn("flex items-center gap-0.5", empty && "invisible")}>
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
              {copied === k ? (
                <Check className="h-3 w-3 text-[var(--success)]" />
              ) : (
                <Copy className="h-3 w-3" />
              )}
            </Button>
          </div>
        </div>
      </li>
    );
  };

  return (
    <ul>
      {/* Single public-URL row, masked + copy-only, at the top when exposed. */}
      {publicUrl && (
        <FieldRow k="PUBLIC_URL" v={publicUrl} sensitive accent />
      )}
      {shownKeys.map((k, i) => (
        <FieldRow
          key={k}
          k={k}
          v={values[k]}
          sensitive={isSensitive(k)}
          last={i === shownKeys.length - 1 && !poolerOff}
        />
      ))}
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
  const external = !!cr?.spec.external && !cr?.spec.pooler?.enabled;
  const pods = useAddonPods(project, external ? "" : addon);
  const list = pods.data?.pods ?? [];
  const ready = list.filter((p) => p.ready).length;
  const restarts = list.reduce((n, p) => n + (p.restarts ?? 0), 0);
  const status: { label: string; detail?: string; tone: string } = external
    ? { label: "External", detail: "Managed outside kuso", tone: "bg-[var(--info)]" }
    : pods.isPending
      ? { label: "Checking…", tone: "bg-[var(--text-tertiary)]" }
      : pods.isError
        ? { label: "Unknown", detail: "Couldn't read pods", tone: "bg-[var(--text-tertiary)]" }
        : list.length === 0
          ? { label: "Down", detail: "No pods running", tone: "bg-[var(--error)]" }
          : ready === list.length
            ? { label: "Healthy", detail: `${ready}/${list.length} pods ready${restarts ? ` · ${restarts} restarts` : ""}`, tone: "bg-[var(--success)]" }
            : { label: "Degraded", detail: `${ready}/${list.length} pods ready`, tone: "bg-[var(--warning)]" };
  const schedule = cr?.spec.backup?.schedule;
  const backups =
    kind !== "postgres"
      ? { label: "Not available", detail: `No managed backups for ${kind || "this kind"}` }
      : schedule
        ? {
            label: describeCron(schedule),
            detail: cr?.spec.backup?.retentionDays ? `Kept ${cr.spec.backup.retentionDays} days` : "Kept forever",
          }
        : { label: "Off", detail: "No backup schedule" };
  const tiles: { k: string; v: React.ReactNode; d?: React.ReactNode }[] = [
    {
      k: "Status",
      v: (
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden className={cn("h-2 w-2 rounded-full", status.tone)} />
          {status.label}
        </span>
      ),
      d: status.detail,
    },
    { k: "Version", v: cr?.spec.version || "Chart default", d: kind },
    {
      k: "Size",
      v: <span className="capitalize">{tier}</span>,
      d: `${storageSize} disk${cr?.spec.ha ? " · HA" : ""}`,
    },
    { k: "Backups", v: backups.label, d: backups.detail },
  ];
  return (
    <section aria-label="Addon health" className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--border-subtle)] sm:grid-cols-4">
      {tiles.map((t) => (
        <div key={t.k} className="min-w-0 bg-[var(--bg-secondary)] px-3 py-2.5">
          <div className="text-xs text-[var(--text-secondary)]">{t.k}</div>
          <div className="mt-0.5 truncate text-sm font-medium text-[var(--text-primary)]">{t.v}</div>
          {t.d && <div className="mt-0.5 truncate text-xs text-[var(--text-tertiary)]">{t.d}</div>}
        </div>
      ))}
      <div className="col-span-2 flex items-center gap-2 bg-[var(--bg-secondary)] px-3 py-2 text-xs text-[var(--text-secondary)] sm:col-span-4">
        <span>Name</span>
        <span className="font-mono text-[var(--text-primary)]">{addon}</span>
        <CopyButton value={addon} what="Addon name" size="xs" />
      </div>
    </section>
  );
}

// describeCron turns the common backup schedules into words; anything
// unusual falls back to the raw expression.
function describeCron(expr: string): string {
  const f = expr.trim().split(/\s+/);
  if (f.length !== 5) return expr;
  const [min, hour, dom, mon, dow] = f;
  const at = /^\d+$/.test(min) && /^\d+$/.test(hour) ? ` at ${hour.padStart(2, "0")}:${min.padStart(2, "0")}` : "";
  if (dom === "*" && mon === "*" && dow === "*" && at) return `Daily${at}`;
  if (dom === "*" && mon === "*" && /^\d$/.test(dow) && at) {
    const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
    return `Weekly, ${days[Number(dow) % 7]}${at}`;
  }
  if (/^\*\/\d+$/.test(hour) && dom === "*" && /^\d+$/.test(min)) return `Every ${hour.slice(2)}h`;
  return expr;
}
