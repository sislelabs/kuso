"use client";

import { AlertTriangle } from "lucide-react";
import { clashingHosts, useIngressDomains } from "@/features/host-clash";

// HostClashHint warns when a host the user is about to claim is already
// served by an Ingress somewhere in the cluster. Admin-only data source;
// renders nothing for non-admins or while loading.
export function HostClashHint({ hosts }: { hosts: string[] }) {
  const domains = useIngressDomains();
  if (!domains.data) return null;
  const clashes = clashingHosts(hosts, domains.data);
  if (clashes.length === 0) return null;
  return (
    <p role="status" className="flex items-start gap-1.5 text-[11px] text-[var(--warning)]">
      <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
      <span>
        Already served by another ingress in this cluster:{" "}
        {clashes.map((h, i) => (
          <span key={h}>
            {i > 0 ? ", " : ""}
            <span className="font-mono">{h}</span>
          </span>
        ))}
        . Traffic for a shared host goes to whichever ingress wins, so pick another name.
      </span>
    </p>
  );
}
