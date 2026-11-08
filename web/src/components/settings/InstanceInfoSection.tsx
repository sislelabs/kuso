"use client";

import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { SettingsSection } from "./SettingsSection";
import { catalogLabel, getInstanceInfo, instanceInfoQueryKey } from "@/features/instance-config";

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1 px-4 py-3 sm:flex-row sm:gap-4 sm:px-5">
      <dt className="w-36 shrink-0 text-sm text-[var(--text-secondary)]">{label}</dt>
      <dd className="min-w-0 break-words text-sm text-[var(--text-primary)]">{children}</dd>
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
    <SettingsSection
      title="Instance info"
      description="Effective values as the server reports them. Change them in the raw spec below."
      flush
    >
      {info.isPending ? (
        <Skeleton className="m-4 h-24" />
      ) : info.isError ? (
        <QueryErrorState what="instance info" error={info.error} onRetry={() => void info.refetch()} className="m-4" />
      ) : (
        <dl className="divide-y divide-[var(--border-subtle)]">
          <Row label="Banner">
            {info.data.banner.show ? (
              <>
                {info.data.banner.text || <span className={muted}>Shown, but the text is empty</span>}
                <span className={`ml-2 font-mono text-xs ${muted}`}>
                  bg {info.data.banner.bgcolor || "-"} · text {info.data.banner.fontcolor || "-"}
                </span>
              </>
            ) : (
              <span className={muted}>Hidden</span>
            )}
          </Row>
          <Row label="Registry">
            {info.data.registry.enabled ? (
              info.data.registry.host ? (
                <span className="font-mono">{info.data.registry.host}</span>
              ) : (
                <span className={muted}>Enabled, no host set</span>
              )
            ) : (
              <span className={muted}>Disabled</span>
            )}
          </Row>
          <Row label="ClusterIssuer"><span className="font-mono">{info.data.clusterIssuer}</span></Row>
          <Row label="Templates">
            <span className={info.data.templates.enabled ? "" : muted}>
              {info.data.templates.enabled ? "Enabled" : (
                <>
                  Disabled <span className="text-xs">(set <code className="font-mono">KUSO_TEMPLATES_ENABLED</code>)</span>
                </>
              )}
            </span>
            {info.data.templates.catalogs.length === 0 ? (
              <span className={`ml-2 ${muted}`}>· no catalogs</span>
            ) : (
              <ul className="mt-1 space-y-0.5 font-mono text-xs text-[var(--text-secondary)]">
                {info.data.templates.catalogs.map((c, i) => (
                  <li key={i}>{catalogLabel(c)}</li>
                ))}
              </ul>
            )}
          </Row>
        </dl>
      )}
    </SettingsSection>
  );
}
