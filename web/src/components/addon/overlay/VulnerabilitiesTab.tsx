"use client";

import { toast } from "sonner";
import { useCanOnProject, Perms } from "@/features/auth";
import { useAddonVulns, useProjectVulns, useRescanVulns, type VulnOwner } from "@/features/vulns";
import { VulnReport } from "@/components/vulns/VulnReport";

export function VulnerabilitiesTab({ project, addon }: { project: string; addon: string }) {
  const owner: VulnOwner = { kind: "addon", project, name: addon };
  const canEdit = useCanOnProject(project, Perms.AddonsWrite);
  const rescan = useRescanVulns(owner);
  const q = useAddonVulns(project, addon, rescan.data?.scanId ?? null);
  // The addon response carries no mode; the project rollup has the
  // instance mode (shared cache with the canvas badges).
  const projectVulns = useProjectVulns(project);

  return (
    <div className="p-6">
      <VulnReport
        owner={owner}
        query={q}
        canEdit={canEdit}
        modeOff={projectVulns.data?.mode === "off"}
        modeOffHint="Vulnerability scanning is disabled for this instance. An admin can enable it under Settings → Vulnerability scanning."
        onRescan={() => rescan.mutate(undefined, { onSuccess: () => toast.success("Rescan queued") })}
        rescanPending={rescan.isPending}
      />
    </div>
  );
}
