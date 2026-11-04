"use client";

import { toast } from "sonner";
import { useCanOnProject, Perms } from "@/features/auth";
import {
  useRescanVulns,
  useServiceVulns,
  useSetServiceVulnPolicy,
  type VulnMode,
  type VulnOwner,
  type VulnPolicy,
} from "@/features/vulns";
import { VulnReport } from "@/components/vulns/VulnReport";

const MODE_HINT: Record<VulnMode, string> = {
  enforce: "Deploys are blocked while the image has fixable critical vulnerabilities.",
  report: "Images are scanned and reported; deploys are never blocked.",
  off: "This service is not scanned.",
};

const POLICIES: VulnPolicy[] = ["inherit", "enforce", "report", "off"];

function PolicyRow({
  project,
  service,
  policy,
  effectiveMode,
  canEdit,
}: {
  project: string;
  service: string;
  policy: VulnPolicy;
  effectiveMode: VulnMode;
  canEdit: boolean;
}) {
  const setPolicy = useSetServiceVulnPolicy(project, service);
  return (
    <section className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-3">
      <div className="min-w-0">
        <p className="text-sm">
          Mode: <span className="font-mono font-semibold">{effectiveMode}</span>
          {policy === "inherit" && <span className="ml-1 text-xs text-[var(--text-tertiary)]">(instance default)</span>}
        </p>
        <p className="text-xs text-[var(--text-secondary)]">{MODE_HINT[effectiveMode]}</p>
      </div>
      {canEdit ? (
        <label className="flex items-center gap-2 text-xs text-[var(--text-secondary)]">
          Policy
          <select
            value={policy}
            disabled={setPolicy.isPending}
            onChange={(e) =>
              setPolicy.mutate(e.target.value as VulnPolicy, {
                onSuccess: (r) => toast.success(`Vulnerability policy set to ${r.mode}`),
              })
            }
            className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
          >
            {POLICIES.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <span className="font-mono text-xs text-[var(--text-tertiary)]">policy: {policy}</span>
      )}
    </section>
  );
}

export function ServiceVulnerabilitiesPanel({
  project,
  service,
  env,
}: {
  project: string;
  service: string;
  // Env-group name (production / staging / …), as the API takes it.
  env: string;
}) {
  const owner: VulnOwner = { kind: "service", project, name: service, env };
  const canEdit = useCanOnProject(project, Perms.ServicesWrite);
  const rescan = useRescanVulns(owner);
  const q = useServiceVulns(project, service, env, rescan.data?.scanId ?? null);
  const data = q.data;

  return (
    <VulnReport
      owner={owner}
      query={q}
      canEdit={canEdit}
      modeOff={data?.effectiveMode === "off"}
      modeOffHint={
        data?.policy === "off"
          ? "This service's policy is off. Choose inherit, report or enforce above to scan it."
          : "Vulnerability scanning is disabled for this instance. An admin can enable it under Settings → Vulnerability scanning."
      }
      header={
        data && (
          <PolicyRow
            project={project}
            service={service}
            policy={data.policy}
            effectiveMode={data.effectiveMode}
            canEdit={canEdit}
          />
        )
      }
      onRescan={() => rescan.mutate(undefined, { onSuccess: () => toast.success("Rescan queued") })}
      rescanPending={rescan.isPending}
    />
  );
}
