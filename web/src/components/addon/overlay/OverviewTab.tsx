"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, Eye, EyeOff, Check } from "lucide-react";
import { addonSecret, useServices } from "@/features/projects";
import { useSubscribedAddonsForServices, useToggleAddonSubscription } from "@/features/addons";
import { Button } from "@/components/ui/button";
import { useCanOnProject, Perms } from "@/features/auth";
import { cn, serviceShortName } from "@/lib/utils";
import { toast } from "sonner";
import { LoadingState } from "@/components/ui/loading-state";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";

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
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <Row label="kind" value={kind || "—"} />
        <Row label="name" value={addon} />
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

      <UsedBy project={project} addon={serviceShortName(project, addon)} />

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
          <h3 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Connection
          </h3>
          <PublicAccessBadge publicTCP={cr?.spec.publicTCP} />
        </header>
        {!canReadSecrets ? (
          <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
            only project admins can view connection details.
          </p>
        ) : conn.isPending ? (
          <LoadingState kind="inline" className="px-3 py-3" />
        ) : conn.isError ? (
          <p className="px-3 py-3 font-mono text-[11px] text-[var(--warning)]">
            {conn.error instanceof Error ? conn.error.message : "load failed"}
          </p>
        ) : Object.keys(conn.data?.values ?? {}).length === 0 ? (
          <p className="px-3 py-3 font-mono text-[11px] text-[var(--text-tertiary)]">
            connection details are still being generated…
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
function UsedBy({ project, addon }: { project: string; addon: string }) {
  const services = useServices(project);
  const names = (services.data ?? []).map((s) => serviceShortName(project, s.metadata.name));
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
        <span className="pt-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          used by
        </span>
        {services.isPending ? (
          <LoadingState kind="inline" />
        ) : names.length === 0 ? (
          <span className="font-mono text-[11px] text-[var(--text-tertiary)]">no services yet</span>
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
  const isSensitive = (k: string) => /url$|password|secret|token/i.test(k);

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
    const visible = !sensitive || !!shown[k];
    return (
      <li
        className={cn(
          "px-3 py-2",
          !last && "border-b border-[var(--border-subtle)]",
          accent && "bg-amber-500/[0.04]",
        )}
      >
        <div className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)_auto] items-center gap-2 sm:grid-cols-[180px_minmax(0,1fr)_auto]">
          <span
            className={cn(
              "truncate font-mono text-[10px] uppercase tracking-widest",
              accent ? "text-amber-300/90" : "text-[var(--text-tertiary)]",
            )}
          >
            {k}
          </span>
          <span
            className={cn(
              "truncate rounded-md bg-[var(--bg-primary)] px-2 py-1 font-mono text-[11px]",
              visible
                ? "text-[var(--text-secondary)]"
                : "text-[var(--text-tertiary)] tracking-widest",
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
              {copied === k ? (
                <Check className="h-3 w-3 text-emerald-500" />
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
      {keys.map((k, i) => (
        <FieldRow
          key={k}
          k={k}
          v={values[k]}
          sensitive={isSensitive(k)}
          last={i === keys.length - 1}
        />
      ))}
    </ul>
  );
}

function Row({
  label,
  value,
  last,
}: {
  label: string;
  value: React.ReactNode;
  last?: boolean;
}) {
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
      <span className="rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
        internal only
      </span>
    );
  }
  return (
    <span className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[10px] text-amber-300">
      public · :{publicTCP.port}
    </span>
  );
}
