"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import type { ProjectDomain } from "./facts";

// The slice of GET .../envs/{env}/domains/status the card needs.
interface DomainCheck {
  host: string;
  dns: { status: string };
  tls: { status: string };
}

// customDomainServing: false only when the status says the domain can't
// answer yet (DNS not pointed here, cert still issuing or failed). An
// unknown answer keeps the custom domain.
export function customDomainServing(d: DomainCheck | undefined): boolean {
  if (!d) return true;
  if (d.dns.status === "mismatch" || d.dns.status === "unresolved") return false;
  return !["issuing", "failed", "missing"].includes(d.tls.status);
}

// useLiveProjectDomain swaps a project's custom headline domain for its
// kuso auto host while the custom one isn't serving. One cached status
// request per card with a custom domain (the server caches DNS checks too).
export function useLiveProjectDomain(project: string, primary: ProjectDomain | undefined): ProjectDomain | undefined {
  const custom = primary?.custom;
  const status = useQuery({
    queryKey: ["projects", project, "primary-domain-status", custom?.service],
    queryFn: () =>
      api<{ domains?: DomainCheck[] }>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(custom?.service ?? "")}/envs/production/domains/status`,
      ),
    enabled: !!custom?.fallback,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  });
  if (!primary || !custom?.fallback || !status.data) return primary;
  const d = status.data.domains?.find((x) => x.host.toLowerCase() === primary.host.toLowerCase());
  return customDomainServing(d) ? primary : custom.fallback;
}
