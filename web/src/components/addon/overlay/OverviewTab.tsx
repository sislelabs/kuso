"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, Eye, EyeOff, Check } from "lucide-react";
import { addonSecret } from "@/features/projects";
import { Button } from "@/components/ui/button";
import { useCan, Perms } from "@/features/auth";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

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
              i < keys.length - 1 && "border-b border-[var(--border-subtle)]",
            )}
          >
            <span className="truncate font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
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
          </li>
        );
      })}
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
