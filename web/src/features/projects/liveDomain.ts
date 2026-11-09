"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { ENV_GROUP_LABEL } from "@/lib/env-group";
import { hostScheme } from "@/lib/host-scheme";
import type { KusoEnvironment } from "@/types/projects";
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

// useCustomHostServing asks the domain status endpoint whether a custom
// host answers yet. undefined = not asked / no answer yet (keep the
// custom host). One cached request per service env, shared by the project
// card, the canvas node and the list view.
function useCustomHostServing(
  project: string,
  service: string | undefined,
  envSegment: string,
  host: string | undefined,
): boolean | undefined {
  const status = useQuery({
    queryKey: ["projects", project, "domain-status", service, envSegment],
    queryFn: () =>
      api<{ domains?: DomainCheck[] }>(
        `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service ?? "")}/envs/${encodeURIComponent(envSegment)}/domains/status`,
      ),
    enabled: !!service && !!host,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  });
  if (!host || !status.data) return undefined;
  const d = status.data.domains?.find((x) => x.host.toLowerCase() === host.toLowerCase());
  return customDomainServing(d);
}

// useLiveProjectDomain swaps a project's custom headline domain for its
// kuso auto host while the custom one isn't serving.
export function useLiveProjectDomain(project: string, primary: ProjectDomain | undefined): ProjectDomain | undefined {
  const custom = primary?.custom;
  const serving = useCustomHostServing(project, custom?.service, "production", custom?.fallback ? primary?.host : undefined);
  if (!primary || !custom?.fallback || serving !== false) return primary;
  return custom.fallback;
}

// useLiveServiceUrl is the same rule for one service: when its URL is the
// env's custom domain and that domain isn't serving yet, show the env's
// kuso auto host instead, exactly as the project card does.
export function useLiveServiceUrl(
  project: string,
  serviceShort: string,
  env: KusoEnvironment | undefined,
  url: string | undefined,
): string | undefined {
  const custom = env?.spec.additionalHosts?.find((h) => !!h);
  const auto = env?.spec.host;
  let isCustom = false;
  if (url && custom) {
    try {
      isCustom = new URL(url).host.toLowerCase() === custom.toLowerCase();
    } catch {
      isCustom = false;
    }
  }
  const envSegment = env?.metadata.labels?.[ENV_GROUP_LABEL] ?? "production";
  const serving = useCustomHostServing(project, serviceShort, envSegment, isCustom && auto ? custom : undefined);
  if (!isCustom || !auto || serving !== false) return url;
  return `${hostScheme(env?.spec.tlsEnabled ?? true, auto)}://${auto}`;
}
