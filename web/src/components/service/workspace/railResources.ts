import { deriveAddonStatus, deriveCronStatus, deriveServiceStatus, type BuildSummary, type NodeStatus } from "@/features/services";
import { cronsForEnv } from "@/features/projects/facts";
import { describeCron } from "@/lib/format-time";
import { serviceShortName } from "@/lib/utils";
import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";

const ENV_LABEL = "kuso.sislelabs.com/env";

export interface RailService {
  name: string; // short name, what the service URL takes
  display: string;
  runtime?: string;
  status: NodeStatus;
}

export interface RailAddon {
  name: string; // short name
  kind: string;
  status: NodeStatus;
}

// railResources picks what the full page's resource rail lists for one
// env-group: the same services and addons the canvas shows for it. A
// shared (unlabelled) resource shows on every env unless the env has its
// own clone of it ("<base>-<env>", or "<base>-pr-N" for previews); an
// env-labelled clone shows only on its own env.
export function railResources(
  project: string,
  env: string,
  services: KusoService[],
  envs: KusoEnvironment[],
  addons: KusoAddon[],
  // Latest build per service short name (useLatestBuilds), so a rail dot
  // says Building when the canvas card does.
  latestBuilds?: Record<string, BuildSummary>,
): { services: RailService[]; addons: RailAddon[] } {
  const suffixes = [`-${env}`];
  if (env.startsWith("preview-")) suffixes.push(`-${env.slice("preview-".length)}`);
  const replaced = (items: { metadata: { name: string; labels?: Record<string, string> } }[]) =>
    new Set(
      env === "production"
        ? []
        : items
            .filter((i) => i.metadata.labels?.[ENV_LABEL] === env)
            .flatMap((i) => {
              const sfx = suffixes.find((s) => i.metadata.name.endsWith(s));
              return sfx ? [i.metadata.name.slice(0, -sfx.length)] : [];
            }),
    );
  const visible = (labels: Record<string, string> | undefined, name: string, swapped: Set<string>) => {
    const v = labels?.[ENV_LABEL];
    if (!v) return !swapped.has(name);
    return env === "production" ? v === "production" : v === env;
  };

  const swappedSvcs = replaced(services);
  const swappedAddons = replaced(addons);
  const envMatches = (e: KusoEnvironment) => {
    const v = e.metadata.labels?.[ENV_LABEL];
    return env === "production" ? !v || v === "production" : v === env;
  };

  const railServices = services
    .filter((s) => visible(s.metadata.labels, s.metadata.name, swappedSvcs))
    .map((s) => {
      const e = envs.find((x) => x.spec.service === s.metadata.name && envMatches(x));
      const name = serviceShortName(project, s.metadata.name);
      return {
        name,
        display: s.spec.displayName?.trim() || name,
        runtime: s.spec.runtime,
        status: deriveServiceStatus({ stopped: !!s.spec.stopped, env: e, latestBuild: latestBuilds?.[name] }),
      };
    })
    .sort((a, b) => a.display.localeCompare(b.display));

  const railAddons = addons
    .filter((a) => visible(a.metadata.labels, a.metadata.name, swappedAddons))
    .map((a) => ({
      name: serviceShortName(project, a.metadata.name),
      kind: a.spec.kind,
      status: deriveAddonStatus(a),
    }))
    .sort((a, b) => a.name.localeCompare(b.name));

  return { services: railServices, addons: railAddons };
}

export interface RailCron {
  name: string; // CR name minus the project prefix
  display: string;
  schedule: string; // in words ("Daily at 03:00 UTC")
  // Short name of the service it runs in; its Crons tab is the cron's home.
  service?: string;
  status: NodeStatus;
}

// railCrons lists the env-group's cron jobs the way the canvas does
// (cronsForEnv), so the rail, the canvas and the phone view agree on
// what's in the project.
export function railCrons(
  project: string,
  env: string,
  crons: ReadonlyArray<{ metadata: { name: string }; spec: { service?: string; schedule?: string; suspend?: boolean; displayName?: string } }>,
  services: KusoService[],
): RailCron[] {
  return cronsForEnv(crons, services, env, project)
    .map((c) => {
      const name = serviceShortName(project, c.metadata.name);
      return {
        name,
        display: c.spec.displayName?.trim() || name,
        schedule: c.spec.schedule ? describeCron(c.spec.schedule) : "",
        service: c.spec.service ? serviceShortName(project, c.spec.service) : undefined,
        status: deriveCronStatus(c.spec),
      };
    })
    .sort((a, b) => a.display.localeCompare(b.display));
}
