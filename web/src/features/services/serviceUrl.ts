import type { KusoEnvironment, KusoService } from "@/types/projects";
import { hostScheme } from "@/lib/host-scheme";
import { isProductionGroup } from "@/lib/env-group";

// serviceUrl is the public URL a service answers on in the given env,
// shared by the canvas node, the list view and the mobile view so all
// three agree. Priority: internal (none) → the env's custom domain →
// the env's own host (staging/preview tabs must not show prod hosts) →
// the project-level custom domain → the operator's status.url.
// No env in the viewed group means nothing serves there: no URL, rather
// than production's service-level domain.
export function serviceUrl(service: KusoService, env?: KusoEnvironment): string | undefined {
  if (service.spec?.internal || !env) return undefined;
  const envTLS = env?.spec?.tlsEnabled ?? true;
  const envCustomHost = env?.spec?.additionalHosts?.find((h) => !!h);
  if (envCustomHost) return `${hostScheme(envTLS, envCustomHost)}://${envCustomHost}`;
  const envHost = env?.spec?.host;
  if (envHost) return `${hostScheme(envTLS, envHost)}://${envHost}`;
  const domain = isProductionGroup(env) ? service.spec.domains?.find((d) => d?.host) : undefined;
  if (domain?.host) return `${(domain.tls ?? true) ? "https" : "http"}://${domain.host}`;
  const fromStatus = env?.status?.url;
  return typeof fromStatus === "string" && fromStatus ? fromStatus : undefined;
}
