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
