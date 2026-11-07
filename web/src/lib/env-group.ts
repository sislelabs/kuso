// Env-group helpers.
//
// A KusoEnvironment's spec.kind is CHART semantics — "production" means
// always-on, "preview" means ephemeral. It is NOT the group identity:
// env-group CLONES (staging / custom) also carry spec.kind="production".
// The REAL group an env belongs to lives on the kuso.sislelabs.com/env
// label (production / staging / preview-pr-N).
//
// Picking "the service's production env" by spec.kind==="production"
// wrongly selects a staging clone → duplicate canvas node, wrong URLs,
// wrong live counts. Select by the label instead.
import type { KusoEnvironment } from "@/types/projects";

export const ENV_GROUP_LABEL = "kuso.sislelabs.com/env";

/** The env-group this env belongs to (production / staging / preview-pr-N), or undefined for legacy CRs missing the label. */
export function envGroupLabel(e: KusoEnvironment): string | undefined {
  return e.metadata?.labels?.[ENV_GROUP_LABEL];
}

/**
 * True iff this env is the production group member. Prefers the
 * kuso.sislelabs.com/env label; falls back to spec.kind==="production"
 * for hand-created / legacy envs that predate the label.
 */
export function isProductionGroup(e: KusoEnvironment): boolean {
  const label = envGroupLabel(e);
  if (label !== undefined) return label === "production";
  return e.spec.kind === "production";
}

/**
 * Display + API name of the env-group this env belongs to. Uses the
 * group label; legacy CRs without it fall back to spec.kind. Never use
 * spec.kind alone for display: a staging clone has kind "production".
 */
export function envGroupName(e: KusoEnvironment | undefined): string {
  if (!e) return "production";
  return envGroupLabel(e) ?? e.spec?.kind ?? "production";
}

export interface EnvOption {
  value: string;
  label: string;
}

/**
 * Env filter options for one service (`fqn` = "<project>-<service>"),
 * production first. The value is what the log store and error feed
 * filter on: the env-group label, else the CR name minus the
 * "<fqn>-" prefix.
 */
export function serviceEnvOptions(envs: KusoEnvironment[], fqn: string): EnvOption[] {
  return envs
    .filter((e) => e.spec.service === fqn)
    .map((e) => {
      if (isProductionGroup(e)) return { value: "production", label: "production" };
      const short =
        envGroupLabel(e) ||
        (fqn && e.metadata.name.startsWith(fqn + "-") ? e.metadata.name.slice(fqn.length + 1) : e.metadata.name);
      return { value: short, label: short };
    })
    .sort((a, b) => {
      if (a.value === "production") return -1;
      if (b.value === "production") return 1;
      return a.label.localeCompare(b.label);
    });
}

/**
 * The wake endpoint's `?env=` for this env: undefined for production
 * (the server default), else the CR name. A preview's CR is
 * "<fqn>-pr-N", which the server can't derive from "preview-pr-N".
 */
export function wakeEnvParam(e: KusoEnvironment | undefined, group: string): string | undefined {
  if (e) return isProductionGroup(e) ? undefined : e.metadata.name;
  return group === "production" ? undefined : group;
}

type Labels = Record<string, string> | undefined;

/**
 * Env-group a KusoService / KusoAddon CR belongs to. Env-group clones
 * carry the env label; an unlabelled CR is the production/shared one.
 */
export function resourceGroup(labels: Labels): string {
  return labels?.[ENV_GROUP_LABEL] || "production";
}

/** True for production/shared CRs (no env label, or label "production"). */
export function isProductionResource(labels: Labels): boolean {
  return resourceGroup(labels) === "production";
}

/**
 * Service/addon membership in an env-group (same rule as svcInGroup in
 * the project view): unlabelled = shared, belongs to every group; a
 * labelled clone belongs only to its own group.
 */
export function resourceInGroup(labels: Labels, group: string): boolean {
  const v = labels?.[ENV_GROUP_LABEL];
  return !v || v === group;
}

/**
 * Base name of an env-group clone ("api-staging" → "api"). Preview
 * clones use the short "-pr-N" suffix for group "preview-pr-N".
 */
export function cloneBaseName(name: string, group: string): string {
  if (group === "production") return name;
  const suffixes = [`-${group}`];
  if (group.startsWith("preview-")) suffixes.push(`-${group.slice("preview-".length)}`);
  for (const s of suffixes) {
    if (name.endsWith(s) && name.length > s.length) return name.slice(0, -s.length);
  }
  return name;
}

/**
 * Names a `${{ name.KEY }}` reference can target from a service in
 * `group`. The server rescopes base names to the group's clone, so
 * clones are offered by base name (deduped against the shared CR) and
 * other groups' clones are dropped.
 */
export function groupReferenceNames(
  items: { name: string; labels: Labels }[],
  group: string,
): string[] {
  const out: string[] = [];
  for (const it of items) {
    if (!resourceInGroup(it.labels, group)) continue;
    const name = it.labels?.[ENV_GROUP_LABEL] ? cloneBaseName(it.name, group) : it.name;
    if (!out.includes(name)) out.push(name);
  }
  return out;
}
