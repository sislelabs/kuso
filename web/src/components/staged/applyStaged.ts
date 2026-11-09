import type { QueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api-client";
import { describeError } from "@/lib/errors";
import {
  getService,
  patchService,
  setServiceEnvValue,
  triggerBuild,
  unsetServiceEnvVar,
  type TriggerBuildResult,
} from "@/features/services/api";
import { applyServiceChanges, type ServiceChangesResult } from "@/features/staged";
import type { StagedService } from "@/features/staged";
import { planService } from "./plan";

export interface ServiceApplyOutcome {
  appliedEnv: string[];
  settingsApplied: boolean;
  // Item key ("env:NAME" | "settings") → error.
  errors: Record<string, string>;
  // The server predates the batch endpoint: items went one by one and the
  // pods may have restarted more than once.
  sequential: boolean;
  // Set when the plan builds: the build the apply started, or why it
  // couldn't start (the settings are applied either way).
  build?: { ok: true; result: TriggerBuildResult } | { ok: false; error: string };
}

// applyStagedService applies one service's staged edits. It reads the
// service fresh so the patch is built against what's deployed now, then
// sends everything in one request (one rollout per environment). An old
// server without the batch endpoint gets the per-item calls instead.
export async function applyStagedService(
  project: string,
  service: string,
  staged: StagedService,
  message: string,
  opts: { neverDeployed?: boolean } = {},
): Promise<ServiceApplyOutcome> {
  const svc = await getService(project, service);
  const plan = planService(svc, staged, opts);
  const errors: Record<string, string> = {};
  if (plan.settingsError) errors.settings = plan.settingsError;
  const settingsStaged = !!staged.settings && Object.keys(staged.settings.fields).length > 0;
  const patch = plan.settingsError ? undefined : plan.patch;

  let res: ServiceChangesResult | null = null;
  let sequential = false;
  if (plan.envSet.length || plan.envUnset.length || patch) {
    try {
      res = await applyServiceChanges(project, service, {
        envSet: plan.envSet,
        envUnset: plan.envUnset,
        patch,
        message: message.trim() || undefined,
      });
    } catch (e) {
      if (!(e instanceof ApiError && (e.status === 404 || e.status === 405))) {
        const msg = describeError(e, "Request failed");
        for (const s of plan.envSet) errors[`env:${s.name}`] = msg;
        for (const n of plan.envUnset) errors[`env:${n}`] = msg;
        if (patch) errors.settings = msg;
        return { appliedEnv: [], settingsApplied: false, errors, sequential };
      }
      sequential = true;
      res = await applySequentially(project, service, plan.envSet, plan.envUnset, patch);
    }
  }

  const appliedEnv: string[] = [];
  let settingsApplied = settingsStaged && !plan.settingsError && !patch; // nothing effective to send
  for (const r of res?.results ?? []) {
    if (r.kind === "patch") {
      if (r.ok) settingsApplied = true;
      else errors.settings = r.error ?? "Failed";
    } else if (r.name) {
      if (r.ok) appliedEnv.push(r.name);
      else errors[`env:${r.name}`] = r.error ?? "Failed";
    }
  }
  if (res?.flushError) {
    // Items are saved but the env CRs weren't rewritten: say so instead of
    // reporting a clean deploy.
    errors.rollout = `Saved, but the rollout didn't start: ${res.flushError}. Redeploy the service to apply.`;
  }
  // Source changes are stored, not built: start the build that deploys them.
  let build: ServiceApplyOutcome["build"];
  if (plan.builds && Object.keys(errors).length === 0) {
    try {
      build = { ok: true, result: await triggerBuild(project, service) };
    } catch (e) {
      build = { ok: false, error: describeError(e, "Request failed") };
    }
  }
  return { appliedEnv, settingsApplied, errors, sequential, build };
}

async function applySequentially(
  project: string,
  service: string,
  envSet: { name: string; value: string }[],
  envUnset: string[],
  patch: Parameters<typeof patchService>[2] | undefined,
): Promise<ServiceChangesResult> {
  const results: ServiceChangesResult["results"] = [];
  for (const s of envSet) {
    try {
      await setServiceEnvValue(project, service, s.name, s.value);
      results.push({ kind: "env-set", name: s.name, ok: true });
    } catch (e) {
      results.push({ kind: "env-set", name: s.name, ok: false, error: describeError(e, "Failed") });
    }
  }
  for (const name of envUnset) {
    try {
      await unsetServiceEnvVar(project, service, name);
      results.push({ kind: "env-unset", name, ok: true });
    } catch (e) {
      const gone = e instanceof ApiError && e.status === 404;
      results.push({ kind: "env-unset", name, ok: gone, error: gone ? undefined : describeError(e, "Failed") });
    }
  }
  if (patch) {
    try {
      await patchService(project, service, patch);
      results.push({ kind: "patch", ok: true });
    } catch (e) {
      results.push({ kind: "patch", ok: false, error: describeError(e, "Failed") });
    }
  }
  return { results, rolledOut: true };
}

export async function refreshServiceQueries(qc: QueryClient, project: string, service: string) {
  await Promise.all([
    qc.invalidateQueries({ queryKey: ["projects", project, "services", service] }),
    qc.invalidateQueries({ queryKey: ["projects", project, "envs"] }),
    qc.invalidateQueries({ queryKey: ["projects", project], exact: true }),
  ]);
}
