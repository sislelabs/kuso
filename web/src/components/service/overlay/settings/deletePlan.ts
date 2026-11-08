import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import { serviceShortName } from "@/lib/utils";

// addonsUsedBy returns the short names of the addons a service uses: the
// ones mounted through its subscription (unset = every addon) plus any
// referenced from an env var (a secretKeyRef on "<addon-fqn>-conn", or a
// literal ${{ addon.KEY }} the server hasn't resolved yet).
export function addonsUsedBy(project: string, svc: KusoService, allAddons: readonly string[]): Set<string> {
  const used = new Set<string>();
  const subscribed = svc.spec.subscribedAddons;
  for (const a of subscribed ?? allAddons) used.add(a);
  for (const ev of svc.spec.envVars ?? []) {
    const ref = (ev.valueFrom as { secretKeyRef?: { name?: string } } | undefined)?.secretKeyRef?.name;
    if (ref?.endsWith("-conn")) used.add(serviceShortName(project, ref.slice(0, -"-conn".length)));
    const m = ev.value?.match(/^\$\{\{\s*([a-zA-Z0-9_-]+)\.[A-Z_][A-Z0-9_]*\s*\}\}$/);
    if (m) used.add(serviceShortName(project, m[1]));
  }
  return new Set([...used].filter((a) => allAddons.includes(a)));
}

// exclusiveAddons are the project addons this service uses and no other
// service does: candidates for an optional follow-up delete. Crons and
// runs aren't checked (they borrow a service's env), so the UI says so.
export function exclusiveAddons(
  project: string,
  service: string,
  services: readonly KusoService[],
  addons: readonly KusoAddon[],
): KusoAddon[] {
  const names = addons.map((a) => serviceShortName(project, a.metadata.name));
  const self = services.find((s) => serviceShortName(project, s.metadata.name) === service);
  if (!self) return [];
  const mine = addonsUsedBy(project, self, names);
  const others = new Set<string>();
  for (const s of services) {
    if (s === self) continue;
    for (const a of addonsUsedBy(project, s, names)) others.add(a);
  }
  return addons.filter((a) => {
    const short = serviceShortName(project, a.metadata.name);
    return mine.has(short) && !others.has(short);
  });
}

export interface EnvSummary {
  name: string;
  label: string;
  kind: "production" | "preview" | "custom";
}

// serviceEnvs lists the environments the server's DeleteService tears
// down: every env CR labelled with this project + service.
export function serviceEnvs(project: string, service: string, envs: readonly KusoEnvironment[]): EnvSummary[] {
  return envs
    .filter((e) => {
      const svc = e.spec.service;
      return svc === service || svc === `${project}-${service}`;
    })
    .map((e) => {
      const group = e.metadata.labels?.["kuso.sislelabs.com/env"];
      const pr = e.spec.pullRequest?.number;
      const label =
        e.spec.kind === "production" ? "production" : e.spec.kind === "preview" && pr ? `PR #${pr}` : group || e.metadata.name;
      return { name: e.metadata.name, label, kind: e.spec.kind };
    })
    .sort((a, b) => (a.kind === "production" ? -1 : b.kind === "production" ? 1 : a.label.localeCompare(b.label)));
}
