"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { api } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { formatBytes } from "@/components/project/list/projectCardModel";

interface InstanceDatabase {
  name: string;
  sizeBytes: number;
  connections: number;
  kind: "addon" | "retained" | "orphan" | "other";
  // The owning role's CONNECTION LIMIT; -1 or absent = none.
  connectionLimit?: number;
  project?: string;
  addon?: string;
  addonCr?: string;
  services: string[];
}

interface InstanceOverview {
  name: string;
  version: string;
  startedAt?: string;
  sizeBytes: number;
  maxConnections: number;
  connections: number;
  databases: InstanceDatabase[];
  missing: { project: string; addon: string; database: string }[];
}

const link =
  "rounded-sm text-[var(--text-primary)] underline decoration-[var(--border-strong)] underline-offset-2 outline-none hover:decoration-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]";

// One shared Postgres as it is right now: read live from the server's
// own catalog, so sizes and connections are real, not configured values.
export function SharedDatabaseOverview({ name }: { name: string }) {
  const q = useQuery<InstanceOverview>({
    queryKey: ["instance-addons", name, "overview"],
    queryFn: () => api(`/api/instance-addons/${encodeURIComponent(name)}/overview`),
    // The server caches this for 60s; polling faster only re-reads its cache.
    refetchInterval: 60_000,
  });

  if (q.isPending) return <Skeleton className="h-64 w-full rounded-md" />;
  if (q.isError) return <QueryErrorState what={`shared database ${name}`} error={q.error} onRetry={() => void q.refetch()} />;

  const d = q.data;
  const owned = d.databases.filter((x) => x.kind === "addon");
  const orphans = d.databases.filter((x) => x.kind === "orphan");
  const retained = d.databases.filter((x) => x.kind === "retained");
  const projects = new Set(owned.map((x) => x.project)).size;
  const largest = Math.max(1, ...d.databases.map((x) => x.sizeBytes));
  const connPct = d.maxConnections > 0 ? d.connections / d.maxConnections : 0;

  return (
    <section aria-label={`Shared database ${d.name}`} className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-[var(--border-subtle)] px-4 py-3">
        <h2 className="font-mono text-sm font-semibold">{d.name}</h2>
        <span className="text-xs text-[var(--text-secondary)]">Postgres {d.version}</span>
        {d.startedAt && (
          <span className="text-xs text-[var(--text-tertiary)]" title={d.startedAt}>
            Started {relativeTime(d.startedAt)}
          </span>
        )}
      </header>

      <dl className="grid grid-cols-2 border-b border-[var(--border-subtle)] sm:grid-cols-4">
        <Stat label="Projects" value={String(projects)} />
        <Stat label="Databases" value={String(owned.length)} />
        <Stat label="Data stored" value={formatBytes(d.sizeBytes)} />
        <Stat
          label="Connections"
          value={`${d.connections} of ${d.maxConnections}`}
          warn={connPct >= 0.8}
          title="Client connections open right now, of the server's max_connections"
        />
      </dl>

      {(orphans.length > 0 || retained.length > 0 || d.missing.length > 0) && (
        <div className="space-y-1 border-b border-[var(--border-subtle)] bg-[var(--warning-subtle)] px-4 py-2.5 text-xs text-[var(--text-primary)]">
          {orphans.length > 0 && (
            <p className="flex gap-2">
              <AlertTriangle aria-hidden className="mt-px size-3.5 shrink-0 text-[var(--warning)]" />
              <span>
                {orphans.length} {orphans.length === 1 ? "database was" : "databases were"} created by kuso, and no addon or
                connection secret references {orphans.length === 1 ? "it" : "them"} now (
                {formatBytes(orphans.reduce((n, x) => n + x.sizeBytes, 0))}). Before you drop one, check no app connects to it
                with a copied URL: its connection count below should stay at 0.
              </span>
            </p>
          )}
          {retained.length > 0 && (
            <p className="flex gap-2">
              <AlertTriangle aria-hidden className="mt-px size-3.5 shrink-0 text-[var(--warning)]" />
              <span>
                {retained.length} {retained.length === 1 ? "database belongs" : "databases belong"} to a deleted addon whose data
                was kept. Adding an addon with the same name in that project reattaches {retained.length === 1 ? "it" : "them"}.
              </span>
            </p>
          )}
          {d.missing.length > 0 && (
            <p className="flex gap-2">
              <AlertTriangle aria-hidden className="mt-px size-3.5 shrink-0 text-[var(--warning)]" />
              <span>
                {d.missing.map((m) => `${m.project}/${m.addon}`).join(", ")} {d.missing.length === 1 ? "points" : "point"} here, but{" "}
                {d.missing.length === 1 ? "its database doesn't" : "their databases don't"} exist on this server.
              </span>
            </p>
          )}
        </div>
      )}

      <div className="overflow-x-auto">
        <table className="w-full min-w-[40rem] text-[13px]">
          <thead>
            <tr className="text-left text-xs text-[var(--text-tertiary)]">
              <th scope="col" className="px-4 py-2 font-medium">Database</th>
              <th scope="col" className="px-4 py-2 font-medium">Belongs to</th>
              <th scope="col" className="px-4 py-2 font-medium">Used by</th>
              <th scope="col" className="w-40 px-4 py-2 font-medium">Size</th>
              <th scope="col" className="px-4 py-2 text-right font-medium">Connections</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[var(--border-subtle)] border-t border-[var(--border-subtle)]">
            {d.databases.map((x) => (
              <tr key={x.name} className={cn(x.kind === "other" && "text-[var(--text-tertiary)]")}>
                <td className="px-4 py-2 font-mono text-xs">{x.name}</td>
                <td className="px-4 py-2">
                  {x.kind === "addon" && x.project && x.addonCr ? (
                    <Link href={`/projects/${x.project}?addon=${encodeURIComponent(x.addonCr)}`} className={link}>
                      {x.project} / {x.addon}
                    </Link>
                  ) : x.kind === "retained" ? (
                    <span title="Addon deleted; data kept for a re-add">{`${x.project} / ${x.addon}, kept after delete`}</span>
                  ) : x.kind === "orphan" ? (
                    <span className="text-[var(--warning)]" title="Created by kuso; no addon uses it now">
                      {x.project ? `${x.project} / ${x.addon}, deleted` : "Deleted addon"}
                    </span>
                  ) : (
                    <span>Not created by kuso</span>
                  )}
                </td>
                <td className="px-4 py-2">
                  {x.services.length > 0 ? (
                    <span className="flex flex-wrap gap-x-2 gap-y-0.5">
                      {x.services.map((s) => (
                        <Link key={s} href={`/projects/${x.project}?service=${encodeURIComponent(s)}`} className={link}>
                          {s}
                        </Link>
                      ))}
                    </span>
                  ) : (
                    <span className="text-[var(--text-tertiary)]">{x.kind === "addon" ? "No service" : "—"}</span>
                  )}
                </td>
                <td className="px-4 py-2">
                  <span className="flex items-center gap-2 tabular-nums">
                    <span aria-hidden className="h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-[var(--bg-tertiary)]">
                      <span
                        className={cn("block h-full rounded-full", x.kind === "orphan" ? "bg-[var(--warning)]" : "bg-[var(--text-tertiary)]")}
                        style={{ width: `${Math.max(2, (x.sizeBytes / largest) * 100)}%` }}
                      />
                    </span>
                    {formatBytes(x.sizeBytes)}
                  </span>
                </td>
                <td
                  className={cn(
                    "px-4 py-2 text-right tabular-nums",
                    x.connectionLimit != null && x.connectionLimit > 0 && x.connections >= x.connectionLimit * 0.8 && "text-[var(--warning)]",
                  )}
                  title={x.connectionLimit != null && x.connectionLimit > 0 ? "Open connections, of this database role's connection limit" : undefined}
                >
                  {x.connectionLimit != null && x.connectionLimit > 0 ? `${x.connections} / ${x.connectionLimit}` : x.connections}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Stat({ label, value, warn, title }: { label: string; value: string; warn?: boolean; title?: string }) {
  return (
    <div className="border-l border-[var(--border-subtle)] px-4 py-3 first:border-l-0 [&:nth-child(3)]:border-l-0 sm:[&:nth-child(3)]:border-l" title={title}>
      <dt className="text-xs text-[var(--text-tertiary)]">{label}</dt>
      <dd className={cn("mt-1 text-lg font-semibold tabular-nums", warn && "text-[var(--warning)]")}>{value}</dd>
    </div>
  );
}
