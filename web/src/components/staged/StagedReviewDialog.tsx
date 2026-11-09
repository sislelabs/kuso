"use client";

import { useMemo, useState, type ReactNode } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, CircleAlert, Loader2, RotateCw, X } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { serviceBlast } from "@/lib/blast-radius";
import { getService, listBuilds } from "@/features/services/api";
import { buildsQueryKey, serviceQueryKey } from "@/features/services/hooks";
import { buildTriggerMessage } from "@/features/services/buildTrigger";
import {
  clearApplied,
  fingerprint,
  getStagedState,
  setStagedErrors,
  stageEnv,
  stageSettings,
  useStagedProject,
  type StagedService,
} from "@/features/staged";
import { buildServicePatch } from "@/components/service/overlay/settings/buildServicePatch";
import type { KusoService } from "@/types/projects";
import { snapshotRollout, trackRollout } from "@/features/projects/rolloutToast";
import { applyStagedService, refreshServiceQueries } from "./applyStaged";
import {
  healthcheckCheck,
  isSourcePatchField,
  overlaySettings,
  patchFieldDiff,
  planService,
  rolloutImpact,
  type HealthcheckCheck,
  type ServicePlan,
} from "./plan";

type Phase = "idle" | "applying" | "done" | "failed";

const PATCH_LABEL: Record<string, string> = {
  port: "Port",
  internal: "Visibility",
  requestLimits: "Request limits",
  scale: "Autoscaling",
  sleep: "Sleep",
  resources: "CPU and memory",
  runtime: "Runtime",
  dockerfile: "Dockerfile",
  watchPaths: "Watch paths",
  image: "Image",
  repo: "Repository",
  placement: "Placement",
  volumes: "Volumes",
  previews: "PR previews",
  waitForCI: "Wait for CI",
  healthcheck: "Health check",
  uptime: "Uptime check",
  release: "Release command",
  securityContext: "Security",
  privateEgress: "Private egress",
  platformApiEgress: "kuso API access",
  snapshotBeforeDeploy: "Snapshot before deploy",
  command: "Start command",
  buildArgs: "Build args",
  publicEnv: "Public env",
  static: "Static build",
  buildpacks: "Buildpacks",
};

function show(value: string | null | undefined, sensitive: boolean): string {
  if (value === undefined) return "hidden";
  if (value === null) return "";
  if (value === "") return '""';
  return sensitive ? fingerprint(value) : value;
}

// Form fields whose edit produces `patchKey`, so a per-row discard drops
// exactly what made that row.
function fieldsBehind(plan: ServicePlan, staged: StagedService, patchKey: string): string[] {
  const fields = staged.settings?.fields ?? {};
  const full = JSON.stringify(plan.patch?.[patchKey as keyof typeof plan.patch]);
  return Object.keys(fields).filter((f) => {
    const rest = { ...fields };
    delete rest[f];
    const res = buildServicePatch(overlaySettings(plan.baseline, rest), plan.baseline, {});
    if ("error" in res) return true;
    return JSON.stringify(res.body[patchKey as keyof typeof res.body]) !== full;
  });
}

export function StagedReviewDialog({
  project,
  open,
  onOpenChange,
}: {
  project: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const staged = useStagedProject(project);
  const services = useMemo(() => Object.keys(staged ?? {}).sort(), [staged]);
  const svcQueries = useQueries({
    queries: services.map((s) => ({
      queryKey: serviceQueryKey(project, s),
      queryFn: () => getService(project, s),
      enabled: open,
    })),
  });
  const svcByName = new Map<string, KusoService | undefined>(services.map((s, i) => [s, svcQueries[i]?.data]));
  const buildQueries = useQueries({
    queries: services.map((s) => ({
      queryKey: buildsQueryKey(project, s),
      queryFn: () => listBuilds(project, s),
      enabled: open,
    })),
  });
  // Nothing has ever built: the first apply has to start the first build.
  const neverDeployed = (i: number) => {
    const builds = buildQueries[i]?.data;
    const svc = svcByName.get(services[i] ?? "");
    return !!builds && !!svc?.spec.repo?.url && !builds.some((b) => b.status?.toLowerCase() === "succeeded");
  };
  const [message, setMessage] = useState("");
  const [phase, setPhase] = useState<Record<string, Phase>>({});
  const [busy, setBusy] = useState(false);
  const [sequentialNote, setSequentialNote] = useState(false);
  const qc = useQueryClient();

  const plans = services.map((s, i) => ({
    service: s,
    staged: staged![s],
    plan: planService(svcByName.get(s), staged![s], { neverDeployed: neverDeployed(i) }),
    neverDeployed: neverDeployed(i),
  }));
  const anyRestart = plans.some((p) => p.plan.restarts);
  const anyBuild = plans.some((p) => p.plan.builds);
  const failingChecks = plans.filter((p) => healthcheckCheck(p.plan, p.staged)?.kind === "failed").map((p) => p.service);
  const blocked = plans.some((p) => p.plan.settingsError);
  // A failed health check test needs a second, explicit "Deploy anyway".
  const [riskAccepted, setRiskAccepted] = useState(false);
  const needsConfirm = failingChecks.length > 0 && !riskAccepted;
  const hasFailures = plans.some((p) => Object.keys(p.staged.errors ?? {}).length > 0);

  const deploy = async () => {
    setRiskAccepted(false);
    setBusy(true);
    let ok = 0;
    let failed = 0;
    // Services go one at a time so a failure is attributed cleanly; each
    // service is a single request and a single rollout.
    for (const service of services) {
      const current = getStagedState()[project]?.[service];
      if (!current) continue;
      setPhase((p) => ({ ...p, [service]: "applying" }));
      const svcPlan = plans.find((p) => p.service === service)?.plan;
      const restarts = svcPlan?.restarts ?? true;
      const builds = svcPlan?.builds ?? false;
      // A build's rollout lands minutes later; the build row tracks it.
      const snap = restarts && !builds ? await snapshotRollout(project, service) : undefined;
      try {
        const out = await applyStagedService(project, service, current, message, {
          neverDeployed: neverDeployed(services.indexOf(service)),
        });
        if (out.sequential) setSequentialNote(true);
        // Refetch before un-staging so the editors re-seed from the new
        // deployed values instead of flashing the old ones.
        await refreshServiceQueries(qc, project, service);
        clearApplied(project, service, { env: out.appliedEnv, settings: out.settingsApplied });
        if (out.build?.ok) {
          void qc.invalidateQueries({ queryKey: buildsQueryKey(project, service) });
          toast.success(buildTriggerMessage(out.build.result, `Build started for ${service}`, service));
        } else if (out.build) {
          toast.error(`${service}: settings applied, but the build didn't start`, {
            description: `${out.build.error}. Use Redeploy to build.`,
          });
        }
        const errCount = Object.keys(out.errors).length;
        setStagedErrors(project, service, errCount ? out.errors : undefined);
        if (errCount) failed++;
        else {
          ok++;
          // "Deployed" waits for the new pods, not the request.
          if (snap) trackRollout(snap, { label: service, qc });
        }
        setPhase((p) => ({ ...p, [service]: errCount ? "failed" : "done" }));
      } catch (e) {
        failed++;
        setStagedErrors(project, service, { request: e instanceof Error ? e.message : "Request failed" });
        setPhase((p) => ({ ...p, [service]: "failed" }));
      }
    }
    setBusy(false);
    if (failed === 0) {
      if (!anyRestart && !anyBuild) toast.success(`Applied changes to ${ok} ${ok === 1 ? "service" : "services"}. No restart needed.`);
      setMessage("");
      setPhase({});
      onOpenChange(false);
    } else {
      toast.error(`${failed} ${failed === 1 ? "service" : "services"} had changes that didn't apply. They're still staged.`);
    }
  };

  const discardEnv = (service: string, name: string) => {
    const s = getStagedState()[project]?.[service];
    if (!s?.env) return;
    stageEnv(project, service, {
      set: s.env.set.filter((e) => e.name !== name),
      unset: s.env.unset.filter((u) => u.name !== name),
    });
  };
  const discardFields = (service: string, fields: string[]) => {
    const s = getStagedState()[project]?.[service];
    if (!s?.settings) return;
    const next = { ...s.settings.fields };
    for (const f of fields) delete next[f];
    stageSettings(project, service, { fields: next, base: s.settings.base });
  };

  const empty = services.length === 0;
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (busy) return;
        setRiskAccepted(false);
        onOpenChange(o);
      }}
    >
      <DialogContent className="flex max-h-[min(88dvh,760px)] flex-col gap-0 p-0 sm:max-w-2xl" showCloseButton={!busy}>
        <DialogHeader className="border-b border-[var(--border-subtle)] px-5 py-4">
          <DialogTitle>Review staged changes</DialogTitle>
          <DialogDescription>
            Each service gets one deploy with everything below. Domains and deletes aren&apos;t staged; they apply
            when you make them.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4">
          {empty && <p className="py-6 text-center text-[13px] text-[var(--text-secondary)]">Nothing staged.</p>}
          {plans.map(({ service, staged: st, plan, neverDeployed: fresh }) => {
            const loading = !svcByName.get(service);
            const impact = rolloutImpact(plan, fresh);
            const errors = st.errors ?? {};
            const ph = phase[service] ?? "idle";
            return (
              <section
                key={service}
                aria-label={`Changes to ${service}`}
                className="rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
              >
                <header className="flex flex-wrap items-center gap-2 border-b border-[var(--border-subtle)] px-3 py-2">
                  <span className="font-mono text-[13px] font-medium text-[var(--text-primary)]">{service}</span>
                  {!loading && (
                    <span
                      className={cn(
                        "rounded-full border px-2 py-px text-xs",
                        plan.builds
                          ? "border-[var(--accent)]/40 text-[var(--text-primary)]"
                          : impact.rolls
                            ? "border-[var(--warning)]/40 text-[var(--text-primary)]"
                            : "border-[var(--border-subtle)] text-[var(--text-secondary)]",
                      )}
                    >
                      {impact.label}
                    </span>
                  )}
                  <span className="ml-auto inline-flex items-center gap-1.5 text-xs text-[var(--text-secondary)]" aria-live="polite">
                    {ph === "applying" && (
                      <>
                        <Loader2 className="h-3.5 w-3.5 animate-spin" /> Applying…
                      </>
                    )}
                    {ph === "done" && (
                      <>
                        <Check className="h-3.5 w-3.5 text-[var(--success)]" /> Applied
                      </>
                    )}
                    {ph === "failed" && (
                      <>
                        <CircleAlert className="h-3.5 w-3.5 text-[var(--error)]" /> Partly failed
                      </>
                    )}
                  </span>
                </header>
                {plan.conflicts.length > 0 && (
                  <p className="flex items-start gap-1.5 border-b border-[var(--border-subtle)] bg-[var(--warning-subtle)] px-3 py-2 text-xs text-[var(--text-primary)]">
                    <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0 text-[var(--warning)]" />
                    Someone changed {plan.conflicts.length === 1 ? "a setting" : "settings"} you staged since you
                    staged {plan.conflicts.length === 1 ? "it" : "them"}. Deploying overwrites their value.
                  </p>
                )}
                {(errors.request || errors.rollout) && (
                  <p role="alert" className="border-b border-[var(--border-subtle)] px-3 py-2 text-xs text-[var(--error)]">
                    {errors.request ?? errors.rollout}
                  </p>
                )}
                <ul className="divide-y divide-[var(--border-subtle)]">
                  {(st.env?.set ?? []).map((e) => (
                    <ItemRow
                      key={`set:${e.name}`}
                      label={e.name}
                      kind={e.before === null ? "Add variable" : "Change variable"}
                      before={e.before === null ? null : show(e.before, e.sensitive)}
                      after={show(e.value, e.sensitive)}
                      error={errors[`env:${e.name}`]}
                      onDiscard={busy ? undefined : () => discardEnv(service, e.name)}
                    />
                  ))}
                  {(st.env?.unset ?? []).map((u) => (
                    <ItemRow
                      key={`unset:${u.name}`}
                      label={u.name}
                      kind="Remove variable"
                      before={show(u.before, u.sensitive)}
                      after={null}
                      error={errors[`env:${u.name}`]}
                      onDiscard={busy ? undefined : () => discardEnv(service, u.name)}
                    />
                  ))}
                  {plan.settingsError && (
                    <li className="px-3 py-2 text-xs text-[var(--error)]">
                      Settings can&apos;t be applied: {plan.settingsError}
                    </li>
                  )}
                  {loading && st.settings && (
                    <li className="px-3 py-2 text-xs text-[var(--text-secondary)]">Loading settings diff…</li>
                  )}
                  {!loading &&
                    plan.patchFields.map((f) => {
                      const [before, after] = patchFieldDiff(plan, f);
                      const check = f === "healthcheck" ? healthcheckCheck(plan, st) : undefined;
                      return (
                        <ItemRow
                          key={`patch:${f}`}
                          label={PATCH_LABEL[f] ?? f}
                          kind="Setting"
                          before={before}
                          after={after}
                          note={
                            plan.builds && isSourcePatchField(f)
                              ? "Applying starts a build from it; the running pods change when that build deploys."
                              : serviceBlast(f)?.message
                          }
                          check={check && <HealthcheckCheckLine check={check} />}
                          error={errors.settings}
                          onDiscard={busy ? undefined : () => discardFields(service, fieldsBehind(plan, st, f))}
                        />
                      );
                    })}
                  {!loading && st.settings && !plan.settingsError && plan.patchFields.length === 0 && (
                    <li className="px-3 py-2 text-xs text-[var(--text-secondary)]">
                      The staged settings match what&apos;s deployed; deploying just clears them.
                    </li>
                  )}
                </ul>
              </section>
            );
          })}
          {sequentialNote && (
            <p className="text-xs text-[var(--text-secondary)]">
              This server doesn&apos;t batch changes yet, so they went one at a time and pods may have restarted more
              than once.
            </p>
          )}
        </div>
        {blocked && !busy && (
          <p role="alert" className="border-t border-[var(--border-subtle)] px-5 py-2 text-xs text-[var(--error)]">
            Fix or discard the settings marked above before deploying.
          </p>
        )}
        {failingChecks.length > 0 && !blocked && !busy && (
          <div className="flex items-start gap-2 border-t border-[var(--border-subtle)] bg-[var(--error-subtle)] px-5 py-2.5 text-xs text-[var(--text-primary)]">
            <CircleAlert className="mt-px h-3.5 w-3.5 shrink-0 text-[var(--error)]" aria-hidden />
            <p>
              The health check path failed its last test on {failingChecks.join(", ")}. New pods won&apos;t become
              ready and the rollout stalls while the previous version keeps serving. Settings apply to every
              environment of the service at once; there&apos;s no staging-only deploy for them.
            </p>
          </div>
        )}
        <footer className="flex flex-col gap-2 border-t border-[var(--border-subtle)] px-5 py-3 sm:flex-row sm:items-center">
          <Input
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="Note for the audit log (optional)"
            aria-label="Note for the audit log"
            maxLength={200}
            className="h-8 flex-1 text-[13px]"
            disabled={busy}
          />
          <div className="flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setRiskAccepted(false);
                onOpenChange(false);
              }}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              variant={needsConfirm || riskAccepted ? "destructive" : "default"}
              onClick={() => (needsConfirm ? setRiskAccepted(true) : void deploy())}
              disabled={busy || empty || blocked}
            >
              {busy ? (
                <>
                  <Loader2 className="animate-spin" /> Applying…
                </>
              ) : needsConfirm ? (
                "Deploy despite failed test…"
              ) : riskAccepted ? (
                "Deploy anyway"
              ) : hasFailures ? (
                <>
                  <RotateCw /> Retry
                </>
              ) : anyBuild ? (
                "Apply & build"
              ) : anyRestart ? (
                "Deploy changes"
              ) : (
                "Apply changes"
              )}
            </Button>
          </div>
        </footer>
      </DialogContent>
    </Dialog>
  );
}

function ItemRow({
  label,
  kind,
  before,
  after,
  note,
  check,
  error,
  onDiscard,
}: {
  label: string;
  kind: string;
  before: string | null;
  after: string | null;
  note?: string;
  check?: ReactNode;
  error?: string;
  onDiscard?: () => void;
}) {
  return (
    <li className="flex items-start gap-2 px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-mono text-xs text-[var(--text-primary)]">{label}</span>
          <span className="text-xs text-[var(--text-tertiary)]">{kind}</span>
        </div>
        <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 font-mono text-xs">
          {before !== null && (
            <span className="min-w-0 break-all text-[var(--text-secondary)] line-through decoration-[var(--text-tertiary)]">
              {before || "(empty)"}
            </span>
          )}
          {before !== null && after !== null && <span className="text-[var(--text-tertiary)]">→</span>}
          {after !== null && <span className="min-w-0 break-all text-[var(--text-primary)]">{after || "(empty)"}</span>}
        </div>
        {note && <p className="mt-1 text-xs text-[var(--text-secondary)]">{note}</p>}
        {check}
        {error && (
          <p role="alert" className="mt-1 text-xs text-[var(--error)]">
            {error}
          </p>
        )}
      </div>
      {onDiscard && (
        <Button variant="ghost" size="icon-xs" onClick={onDiscard} aria-label={`Discard ${label}`} title="Discard">
          <X />
        </Button>
      )}
    </li>
  );
}

function HealthcheckCheckLine({ check }: { check: HealthcheckCheck }) {
  if (check.kind === "untested") {
    return (
      <p className="mt-1 text-xs text-[var(--text-secondary)]">
        Not tested. Use Test path in the Health check card to check <span className="font-mono">{check.path}</span>{" "}
        answers before deploying.
      </p>
    );
  }
  const failed = check.kind === "failed";
  return (
    <p className={cn("mt-1 inline-flex items-start gap-1.5 text-xs", failed ? "text-[var(--error)]" : "text-[var(--text-secondary)]")}>
      {failed ? (
        <CircleAlert className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden />
      ) : (
        <Check className="mt-px h-3.5 w-3.5 shrink-0 text-[var(--success)]" aria-hidden />
      )}
      <span>
        Last test of <span className="font-mono">{check.path}</span>: {check.summary}
        {failed ? ". Pods answering like this never become ready." : "."}
      </span>
    </p>
  );
}
