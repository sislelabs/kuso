"use client";

import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { catalogLabel, getInstanceInfo, instanceInfoQueryKey } from "@/features/instance-config";

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid items-start gap-1 px-4 py-2 sm:grid-cols-[140px_1fr] sm:gap-3">
      <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">{label}</div>
      <div className="min-w-0 break-words font-mono text-[12px] text-[var(--text-primary)]">{children}</div>
    </div>
  );
}

const muted = "text-[var(--text-tertiary)]";

// InstanceInfoSection is the read-only view of what /api/config/{banner,
// registry,clusterissuer,templates} report. These come from the Kuso CR
// spec; edit them in the raw spec below.
export function InstanceInfoSection() {
  const info = useQuery({ queryKey: instanceInfoQueryKey, queryFn: getInstanceInfo });

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="border-b border-[var(--border-subtle)] px-4 py-2.5">
        <h2 className="text-sm font-semibold tracking-tight">Instance info</h2>
        <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
          Effective values as the server reports them (read-only).
        </p>
      </header>
      {info.isPending ? (
        <Skeleton className="m-3 h-24" />
      ) : info.isError ? (
        <QueryErrorState what="instance info" error={info.error} onRetry={() => void info.refetch()} className="m-3" />
      ) : (
        <div className="divide-y divide-[var(--border-subtle)]">
          <Row label="Banner">
            {info.data.banner.show ? (
              <>
                {info.data.banner.text || <span className={muted}>(empty text)</span>}
                <span className={`ml-2 text-[10px] ${muted}`}>
                  bg {info.data.banner.bgcolor || "-"} · text {info.data.banner.fontcolor || "-"}
                </span>
              </>
            ) : (
              <span className={muted}>hidden</span>
            )}
          </Row>
          <Row label="Registry">
            {info.data.registry.enabled ? (
              info.data.registry.host || <span className={muted}>enabled, no host set</span>
            ) : (
              <span className={muted}>disabled</span>
            )}
          </Row>
          <Row label="ClusterIssuer">{info.data.clusterIssuer}</Row>
          <Row label="Templates">
            <span className={info.data.templates.enabled ? "" : muted}>
              {info.data.templates.enabled ? "enabled" : "disabled (KUSO_TEMPLATES_ENABLED)"}
            </span>
            {info.data.templates.catalogs.length === 0 ? (
              <span className={`ml-2 ${muted}`}>· no catalogs</span>
            ) : (
              <ul className="mt-1 space-y-0.5 text-[11px] text-[var(--text-secondary)]">
                {info.data.templates.catalogs.map((c, i) => (
                  <li key={i}>{catalogLabel(c)}</li>
                ))}
              </ul>
            )}
          </Row>
        </div>
      )}
    </section>
  );
}
