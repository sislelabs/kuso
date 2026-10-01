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
