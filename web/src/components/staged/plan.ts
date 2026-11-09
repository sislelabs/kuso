import type { PatchServiceBody } from "@/features/services/api";
import type { StagedService } from "@/features/staged";
import type { KusoService } from "@/types/projects";
import { fromSvc, type FormState } from "@/components/service/overlay/settings/_primitives";
import { buildServicePatch, fieldDiffValues } from "@/components/service/overlay/settings/buildServicePatch";

// Patch fields that rewrite the pod template (docs/EDIT_SAFETY.md), so
// applying them rolls the pods. Everything else (scale, sleep, uptime,
// repo, release hook, request limits, …) lands without a restart.
const ROLLING_PATCH_FIELDS = new Set([
  "image",
  "port",
  "resources",
  "securityContext",
  "healthcheck",
  "command",
  "placement",
  "volumes",
  "runtime",
  "privateEgress",
  "platformApiEgress",
  "publicEnv",
]);

// Patch fields that change what gets built. The server stores them but
// doesn't rebuild, so the review triggers a build when any of them is
// staged.
const SOURCE_PATCH_FIELDS = new Set(["repo", "dockerfile", "runtime", "buildArgs", "static", "buildpacks"]);

export function isSourcePatchField(field: string): boolean {
  return SOURCE_PATCH_FIELDS.has(field);
}

export interface ServicePlan {
  envSet: { name: string; value: string }[];
  envUnset: string[];
  patch?: PatchServiceBody;
  patchFields: string[];
  baseline: FormState;
  target: FormState;
  // Settings that can't be built into a patch (validation); the settings
  // item fails with this and the env items still apply.
  settingsError?: string;
  // Form fields someone else changed since they were staged.
  conflicts: string[];
  restarts: boolean;
  // Applying should also start a build: a source/build setting changed, or
  // the service has never deployed. Never for image services.
  builds: boolean;
}

// overlaySettings is the deployed form state with the staged fields on top.
export function overlaySettings(baseline: FormState, fields: Record<string, unknown> | undefined): FormState {
  if (!fields) return baseline;
  const out = { ...baseline } as unknown as Record<string, unknown>;
  for (const [k, v] of Object.entries(fields)) if (k in out) out[k] = v;
  return out as unknown as FormState;
}

function same(a: unknown, b: unknown): boolean {
  return a === b || JSON.stringify(a) === JSON.stringify(b);
}

export function planService(
  svc: KusoService | undefined,
  staged: StagedService,
  opts: { neverDeployed?: boolean } = {},
): ServicePlan {
  const baseline = fromSvc(svc);
  const fields = staged.settings?.fields;
  const target = overlaySettings(baseline, fields);
  const conflicts = Object.keys(fields ?? {}).filter((k) => {
    const base = staged.settings?.base;
    return base !== undefined && k in base && !same(base[k], (baseline as unknown as Record<string, unknown>)[k]);
  });
  let patch: PatchServiceBody | undefined;
  let settingsError: string | undefined;
  if (fields && Object.keys(fields).length) {
    const res = buildServicePatch(target, baseline, {
      command: svc?.spec.command,
      releaseCommand: svc?.spec.release?.command,
    });
    if ("error" in res) settingsError = res.error;
    else if (Object.keys(res.body).length) patch = res.body;
  }
  const patchFields = Object.keys(patch ?? {});
  const envSet = (staged.env?.set ?? []).map((e) => ({ name: e.name, value: e.value }));
  const envUnset = (staged.env?.unset ?? []).map((u) => u.name);
  const builtFromSource = !!svc && target.runtime !== "image";
  const builds =
    builtFromSource &&
    !settingsError &&
    (patchFields.some((f) => SOURCE_PATCH_FIELDS.has(f)) ||
      (!!opts.neverDeployed && (envSet.length > 0 || envUnset.length > 0 || patchFields.length > 0)));
  return {
    envSet,
    envUnset,
    patch,
    patchFields,
    baseline,
    target,
    settingsError,
    conflicts,
    restarts: envSet.length > 0 || envUnset.length > 0 || patchFields.some((f) => ROLLING_PATCH_FIELDS.has(f)),
    builds,
  };
}

// rolloutImpact is the one line the review shows for what applying a
// service's changes does to its pods. A build always ends in a rollout,
// so "no restart" is never said next to one; env/template edits on a
// service with pods roll them now and the build rolls them again later.
export function rolloutImpact(plan: Pick<ServicePlan, "builds" | "restarts">, neverDeployed = false): {
  label: string;
  rolls: boolean;
} {
  if (plan.builds) {
    return plan.restarts && !neverDeployed
      ? { label: "Restarts pods now and again after the build", rolls: true }
      : { label: "Builds, then deploys", rolls: true };
  }
  return plan.restarts ? { label: "Restarts pods once", rolls: true } : { label: "No restart", rolls: false };
}

export function patchFieldDiff(plan: ServicePlan, field: string): [string, string] {
  return fieldDiffValues(field, plan.baseline, plan.target);
}

export type HealthcheckCheck =
  | { kind: "passed"; summary: string; path: string }
  | { kind: "failed"; summary: string; path: string }
  | { kind: "untested"; path: string };

// healthcheckCheck says what the last "Test path" run found for the
// health check this plan deploys. Undefined when the plan doesn't turn an
// HTTP check on or change its path/port.
export function healthcheckCheck(plan: ServicePlan, staged: StagedService): HealthcheckCheck | undefined {
  if (!plan.patchFields.includes("healthcheck") || !plan.target.hcEnabled) return undefined;
  const path = plan.target.hcPath.trim();
  if (!path) return undefined;
  const port = plan.target.hcPort.trim() || plan.target.port.trim();
  const t = staged.healthcheckTest;
  if (!t || t.path !== path || t.port !== port) return { kind: "untested", path };
  return { kind: t.passed ? "passed" : "failed", summary: t.summary, path };
}

// reviewItemCount is how many rows the review shows for a service: one
// per variable, one per changed setting (several form fields can make one
// "Health check" row), plus the settings error if the patch can't build.
// Without the deployed service (still loading) it falls back to the raw
// staged count.
export function reviewItemCount(svc: KusoService | undefined, staged: StagedService | undefined): number {
  if (!staged) return 0;
  const env = (staged.env?.set.length ?? 0) + (staged.env?.unset.length ?? 0);
  const fields = Object.keys(staged.settings?.fields ?? {}).length;
  if (!svc || fields === 0) return env + fields;
  const plan = planService(svc, staged);
  return env + plan.patchFields.length + (plan.settingsError ? 1 : 0);
}
