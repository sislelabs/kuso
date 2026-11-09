"use client";

import { hasPublicIngress, stopConsequence } from "./stopCopy";
import { firstRunState } from "./firstRun";
import { cloneElement, useEffect, useId, useState } from "react";
import Link from "next/link";
import { AlertTriangle, BellPlus, Check, Loader2, MoreHorizontal, Package, Play, Rocket, RotateCw, Square, Undo2, Wrench } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { snapshotRollout, trackRollout } from "@/features/projects/rolloutToast";
import { toastError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import {
  buildTriggerMessage,
  useBuilds,
  useRestartService,
  useService,
  useStartService,
  useStopService,
  usePatchService,
  useTriggerBuild,
} from "@/features/services";
import { parseImageRef } from "@/features/services/imageRef";
import { Input } from "@/components/ui/input";
import { useEnvironments, useProject } from "@/features/projects";
import { useCan, useCanOnProject, Perms } from "@/features/auth";
import { envGroupName, isProductionGroup } from "@/lib/env-group";
import { cn } from "@/lib/utils";
import type { KusoEnvironment } from "@/types/projects";
import type { FixFirst } from "../workspace/fixFirst";
import { useRolloutRevert } from "./rolloutRevert";
import { useImageCheck } from "@/features/builds";

const NO_ACCESS = "Requires editor access on this project";

// Prefilled alert-rule links. The alerts page reads
// ?new=1&kind=&project=&service=&env= and opens its create form with them.
const ALERT_PRESETS = [
  { kind: "http_5xx_rate", label: "Error rate is high", hint: "Share of 5xx responses crosses a threshold" },
  { kind: "http_p95_latency", label: "Responses get slow", hint: "p95 latency crosses a threshold" },
  { kind: "log_match", label: "Logs match a pattern", hint: "A log line matches text you choose" },
] as const;

export function alertRuleHref(project: string, service: string, env: string, kind: string): string {
  const q = new URLSearchParams({ new: "1", kind, project, service, env });
  return `/settings/alerts?${q.toString()}`;
}

// What each action does, in one place, so the header tooltips and the
// confirm dialogs can't drift apart.
const EXPLAIN = {
  redeploy: (branch: string) =>
    `Builds the latest commit on ${branch} and rolls it out. The current version keeps serving until the new one is ready.`,
  restart: (group: string) =>
    `Replaces the ${group} pods with fresh ones on the same image. No build, no code change.`,
  start: "Brings the service back online on its current image.",
};

// useRedeploy builds the env's branch head. Production asks first (it's a
// real rollout); other env groups fire straight away.
export function useRedeploy(project: string, service: string, env?: KusoEnvironment) {
  const trigger = useTriggerBuild(project, service);
  const builds = useBuilds(project, service);
  const svc = useService(project, service);
  const proj = useProject(project);
  const [confirming, setConfirming] = useState(false);
  const group = envGroupName(env);
  const branch =
    env?.spec?.branch || svc.data?.spec.repo?.defaultBranch || proj.data?.project.spec.defaultRepo?.defaultBranch || "";
  const repoUrl = svc.data?.spec.repo?.url || proj.data?.project.spec.defaultRepo?.url;
  // "Deploy" until something has gone live, "Redeploy" after.
  // Same reading as the overlay's first-run state: a worker that reuses
  // another service's image has no builds of its own but has run.
  const neverDeployed = env
    ? firstRunState(env, builds.data).kind === "never"
    : !!builds.data && !builds.data.some((b) => b.status?.toLowerCase() === "succeeded");
  const verb = neverDeployed ? "Deploy" : "Redeploy";

  const run = async () => {
    try {
      const res = await trigger.mutateAsync(env?.spec?.branch ? { branch: env.spec.branch } : {});
      toast.success(buildTriggerMessage(res, `${verb} started`));
    } catch (e) {
      toastError(e, "Failed to trigger build");
    }
  };
  const request = () => {
    if (env && isProductionGroup(env)) setConfirming(true);
    else void run();
  };

  const dialog = (
    <ConfirmDialog
      open={confirming}
      title={`${verb} ${group}?`}
      destructive={false}
      confirmLabel="Build & deploy"
      body={
        <span>
          Builds the latest commit on{" "}
          <span className="font-mono text-[var(--text-primary)]">{branch || "the default branch"}</span>
          {repoUrl ? (
            <>
              {" "}
              of <span className="font-mono text-[var(--text-primary)]">{repoUrl.replace(/^https?:\/\/(www\.)?/, "")}</span>
            </>
          ) : null}{" "}
          and rolls it out to <strong>{group}</strong> once it succeeds. The current version keeps serving until then.
        </span>
      }
      pending={trigger.isPending}
      onConfirm={() => {
        setConfirming(false);
        void run();
      }}
      onCancel={() => setConfirming(false)}
    />
  );

  return {
    request,
    dialog,
    pending: trigger.isPending,
    verb,
    explain: EXPLAIN.redeploy(branch || "the default branch"),
    isImage: svc.data?.spec.runtime === "image",
  };
}

// ServiceHeaderActions is the overlay header's action row: Redeploy is the
// one visible action (Start too while stopped); Restart and Stop sit in
// the ⋯ menu, each with its explanation, so a mis-tap can't take the
// service down.
export function ServiceHeaderActions({
  project,
  service,
  env,
  stopped,
  showRestart,
  hideDeploy = false,
  neverRan: neverRanProp = false,
  fix,
  onFix,
}: {
  project: string;
  service: string;
  env?: KusoEnvironment;
  stopped: boolean;
  showRestart: boolean;
  // The never-deployed banner carries Deploy itself.
  hideDeploy?: boolean;
  // Nothing has ever run here (overlay's first-run state).
  neverRan?: boolean;
  // A known failure's fix: it becomes the primary action, since deploying
  // again would fail the same way.
  fix?: FixFirst;
  onFix?: (fix: FixFirst) => void;
}) {
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const redeploy = useRedeploy(project, service, env);
  const restart = useRestartService(project, service);
  const qc = useQueryClient();
  const stop = useStopService(project, service);
  const start = useStartService(project, service);
  const [confirm, setConfirm] = useState<"restart" | "stop" | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const group = envGroupName(env);
  const canAlert = useCan(Perms.SettingsAdmin);
  // A failing rollout with a known previous config: undoing the change is
  // the action that matters, so it leads; the fix-forward stays beside it.
  const rollback = useRolloutRevert(project, service, env);
  const revertFirst = (rollback.available && canWrite) || rollback.reverting;

  const onRestart = async () => {
    try {
      const snap = await snapshotRollout(project, service, group);
      await restart.mutateAsync(group);
      trackRollout(snap, {
        label: service,
        qc,
        rollingText: `Restarting ${service} (${group})…`,
        doneText: `Restarted ${service} (${group})`,
      });
      setConfirm(null);
    } catch (e) {
      toastError(e, "Restart failed");
    }
  };
  const onStop = async () => {
    try {
      await stop.mutateAsync();
      toast.success(`Stopped ${service}`);
      setConfirm(null);
    } catch (e) {
      toastError(e, "Failed to stop service");
    }
  };
  const onStart = async () => {
    try {
      await start.mutateAsync();
      toast.success(`Starting ${service}`);
    } catch (e) {
      toastError(e, "Failed to start service");
    }
  };

  // A service that has never had an image has no pods to restart or stop,
  // so those actions aren't offered at all.
  const neverRan = neverRanProp || env?.status?.state === "no_image";
  const restartBtn = showRestart && !stopped && !neverRan;
  const stopBtn = !stopped && !neverRan;
  const restartExplain = !canWrite ? NO_ACCESS : EXPLAIN.restart(group);
  const svcSpec = useService(project, service).data?.spec;
  const stopText = stopConsequence(hasPublicIngress(env, !!svcSpec?.internal, svcSpec?.runtime));
  const stopExplain = !canWrite ? NO_ACCESS : stopText;
  const menuItem =
    "flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left hover:bg-[var(--bg-tertiary)] disabled:opacity-50";

  return (
    <>
      {revertFirst && (
        <Explained
          label="Revert"
          explain={
            rollback.reverting
              ? "The previous config is being re-applied; the failing pods are on their way out."
              : `Undo ${rollback.changeLabel || "the change"} that started this rollout. The previous version is still serving.`
          }
        >
          <Button size="sm" onClick={rollback.request} disabled={rollback.pending || rollback.reverting} className="h-7 px-2.5 text-xs">
            {rollback.reverting ? <Loader2 className="h-3 w-3 motion-safe:animate-spin" /> : <Undo2 className="h-3 w-3" />}
            {rollback.reverting ? "Reverting…" : "Revert"}
          </Button>
        </Explained>
      )}
      {fix && fix.id !== "image" && onFix && (
        <Button size="sm" variant={revertFirst ? "secondary" : undefined} onClick={() => onFix(fix)} className="h-7 px-2.5 text-xs">
          <Wrench className="h-3 w-3" />
          {fix.label}
        </Button>
      )}
      {redeploy.isImage && !hideDeploy && (
        <DeployImageControl
          project={project}
          service={service}
          canWrite={canWrite}
          label={fix?.id === "image" ? `${fix.label}…` : undefined}
          secondary={(!!fix && fix.id !== "image") || revertFirst}
        />
      )}
      {/* A missing branch fails every build: no Redeploy until it's changed. */}
      {!redeploy.isImage && !hideDeploy && fix?.id !== "branch" && (
        <Explained label={redeploy.verb} explain={canWrite ? redeploy.explain : NO_ACCESS}>
          <Button
            size="sm"
            variant={fix || revertFirst ? "secondary" : undefined}
            onClick={redeploy.request}
            disabled={!canWrite || redeploy.pending}
            // Phones keep the bar to Revert and the fix while it's failing.
            className={cn("h-7 px-2.5 text-xs", revertFirst && "max-sm:hidden")}
          >
            <Rocket className="h-3 w-3" />
            {redeploy.pending ? "Starting…" : redeploy.verb}
          </Button>
        </Explained>
      )}

      {/* Start stays inline (it's the way back); Restart, Stop and the
          alert shortcuts live in the ⋯ menu at every width. */}
      <div className="flex items-center gap-1.5">
        {stopped && (
          <Explained label="Start" explain={canWrite ? EXPLAIN.start : NO_ACCESS}>
            <Button size="sm" variant="secondary" onClick={onStart} disabled={!canWrite || start.isPending} className="h-7 px-2.5 text-xs">
              <Play className="h-3 w-3" />
              {start.isPending ? "Starting…" : "Start"}
            </Button>
          </Explained>
        )}
        {(restartBtn || stopBtn || canAlert) && (
          <Popover open={menuOpen} onOpenChange={setMenuOpen}>
            <PopoverTrigger
              aria-label="More service actions"
              className="inline-flex h-7 w-8 items-center justify-center rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
            >
              <MoreHorizontal className="h-4 w-4" />
            </PopoverTrigger>
            <PopoverContent align="end" className="w-72 gap-0.5 rounded-lg p-1">
              {restartBtn && (
                <button
                  type="button"
                  disabled={!canWrite || restart.isPending}
                  className={menuItem}
                  onClick={() => {
                    setMenuOpen(false);
                    setConfirm("restart");
                  }}
                >
                  <RotateCw className={cn("mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-secondary)]", restart.isPending && "motion-safe:animate-spin")} />
                  <span className="min-w-0">
                    <span className="block text-sm text-[var(--text-primary)]">Restart</span>
                    <span className="block text-xs text-[var(--text-secondary)]">
                      {restartExplain}
                    </span>
                  </span>
                </button>
              )}
              {stopBtn && (
                <button
                  type="button"
                  disabled={!canWrite || stop.isPending}
                  className={menuItem}
                  onClick={() => {
                    setMenuOpen(false);
                    setConfirm("stop");
                  }}
                >
                  <Square className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--danger)]" />
                  <span className="min-w-0">
                    <span className="block text-sm text-[var(--danger)]">Stop service</span>
                    <span className="block text-xs text-[var(--text-secondary)]">{stopExplain}</span>
                  </span>
                </button>
              )}
              {canAlert && (
                <div role="group" aria-labelledby="svc-alert-menu-heading" className={cn((restartBtn || stopBtn) && "mt-0.5 border-t border-[var(--border-subtle)] pt-1")}>
                  <div id="svc-alert-menu-heading" className="flex items-center gap-1.5 px-2 pb-1 pt-1.5 text-xs font-medium text-[var(--text-secondary)]">
                    <BellPlus aria-hidden className="h-3.5 w-3.5" />
                    Alert me when…
                  </div>
                  {ALERT_PRESETS.map((p) => (
                    <Link
                      key={p.kind}
                      href={alertRuleHref(project, service, group, p.kind)}
                      onClick={() => setMenuOpen(false)}
                      className={cn(menuItem, "pl-7")}
                    >
                      <span className="min-w-0">
                        <span className="block text-sm text-[var(--text-primary)]">{p.label}</span>
                        <span className="block text-xs text-[var(--text-secondary)]">{p.hint}</span>
                      </span>
                    </Link>
                  ))}
                </div>
              )}
            </PopoverContent>
          </Popover>
        )}
      </div>

      {redeploy.dialog}
      {rollback.dialog}
      <ConfirmDialog
        open={confirm === "restart"}
        title={`Restart ${service}?`}
        body={EXPLAIN.restart(group)}
        confirmLabel="Restart"
        destructive={false}
        pending={restart.isPending}
        onConfirm={onRestart}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmDialog
        open={confirm === "stop"}
        title={`Stop ${service}?`}
        body={stopText}
        confirmLabel="Stop service"
        destructive
        pending={stop.isPending}
        onConfirm={onStop}
        onCancel={() => setConfirm(null)}
      />
    </>
  );
}

// DeployImageControl is the image service's deploy: it has no build, so
// shipping means pointing the service at another tag. Accepts a bare tag
// or a pasted full reference (repo:tag); digests aren't supported because
// the chart renders "<repository>:<tag>".
function DeployImageControl({
  project,
  service,
  canWrite,
  label,
  secondary,
}: {
  project: string;
  service: string;
  canWrite: boolean;
  // "Change image…" when the current image can't be pulled.
  label?: string;
  secondary?: boolean;
}) {
  const svc = useService(project, service).data;
  const envs = useEnvironments(project).data;
  const patch = usePatchService(project, service);
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const [touched, setTouched] = useState(false);
  const inputId = useId();
  const repository = svc?.spec.image?.repository ?? "";
  const currentTag = svc?.spec.image?.tag || "latest";
  const fqn = `${project}-${service}`;
  const groups = [...new Set((envs ?? []).filter((e) => e.spec.service === fqn).map((e) => envGroupName(e)))];

  const parsed = parseTarget(value, repository);
  const error = touched || value ? parsed.error : undefined;
  const unchanged = !parsed.error && parsed.repository === repository && parsed.tag === currentTag;
  // Ask the registry once typing settles: a typo'd tag is otherwise only
  // found when the rollout can't pull it.
  const [settled, setSettled] = useState({ repository: "", tag: "" });
  useEffect(() => {
    const t = setTimeout(() => setSettled({ repository: parsed.repository, tag: parsed.tag }), 400);
    return () => clearTimeout(t);
  }, [parsed.repository, parsed.tag]);
  const typing = settled.repository !== parsed.repository || settled.tag !== parsed.tag;
  const check = useImageCheck(project, service, settled.repository, settled.tag, open && !parsed.error && !unchanged && !typing);
  const missing = !typing && !parsed.error && !unchanged && check.data?.status === "not_found";

  const submit = async () => {
    setTouched(true);
    if (parsed.error || !parsed.tag) return;
    try {
      const snap = await snapshotRollout(project, service);
      await patch.mutateAsync({ image: { repository: parsed.repository, tag: parsed.tag } });
      trackRollout(snap, { label: `${service} on ${parsed.tag}`, qc, startedText: `Deploying ${parsed.repository}:${parsed.tag}` });
      setOpen(false);
    } catch (e) {
      toastError(e, "Couldn't deploy the image");
    }
  };

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (o) {
          setValue(currentTag);
          setTouched(false);
        }
      }}
    >
      <Explained
        label={label ? "Change image" : "Deploy image"}
        explain={
          !canWrite
            ? NO_ACCESS
            : label
              ? "The current image can't be pulled. Roll out a tag or image that exists."
              : "Roll out another tag of this image. No build runs."
        }
      >
        <PopoverTrigger
          render={
            <Button
              size="sm"
              variant={secondary ? "secondary" : undefined}
              disabled={!canWrite || patch.isPending}
              className="h-7 px-2.5 text-xs"
            >
              <Package className="h-3 w-3" />
              {patch.isPending ? "Deploying…" : (label ?? "Deploy image…")}
            </Button>
          }
        />
      </Explained>
      <PopoverContent align="end" className="w-80 gap-0 rounded-lg p-3">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          className="space-y-2.5"
        >
          <div className="space-y-1">
            <label htmlFor={inputId} className="block text-xs font-medium text-[var(--text-primary)]">
              Tag to deploy
            </label>
            <p className="text-xs text-[var(--text-tertiary)]">
              Running{" "}
              <span className="break-all font-mono">
                {repository || "—"}:<span className="text-[var(--text-secondary)]">{currentTag}</span>
              </span>
            </p>
          </div>
          <Input
            id={inputId}
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onBlur={() => setTouched(true)}
            spellCheck={false}
            autoComplete="off"
            placeholder="e.g. 1.4.2 or ghcr.io/acme/web:1.4.2"
            aria-invalid={!!error || undefined}
            aria-describedby={`${inputId}-hint`}
            className="h-8 font-mono text-[13px]"
          />
          <p id={`${inputId}-hint`} className={cn("text-xs", error ? "text-[var(--danger)]" : "text-[var(--text-secondary)]")}>
            {error ??
              `Rolls out to ${groups.length > 1 ? `all ${groups.length} environments (${groups.join(", ")})` : (groups[0] ?? "production")}. The current version serves until the new pods are ready.`}
          </p>
          {!error && !unchanged && (
            <p role="status" aria-live="polite" className="flex min-h-4 items-start gap-1.5 text-xs">
              {typing || check.isFetching ? (
                <span className="text-[var(--text-tertiary)]">Checking the registry…</span>
              ) : missing ? (
                <span className="flex items-start gap-1.5 text-[var(--warning)]" title={check.data?.detail}>
                  <AlertTriangle aria-hidden className="mt-px h-3.5 w-3.5 shrink-0" />
                  <span>Tag not found in the registry. Deploying it would fail to pull.</span>
                </span>
              ) : check.data?.status === "found" ? (
                <span className="flex items-center gap-1.5 text-[var(--text-secondary)]">
                  <Check aria-hidden className="h-3.5 w-3.5 text-[var(--success)]" />
                  Found in the registry
                </span>
              ) : null}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="secondary" className="h-7 px-2.5 text-xs" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              type="submit"
              size="sm"
              variant={missing ? "secondary" : undefined}
              className="h-7 px-2.5 text-xs"
              disabled={!!parsed.error || unchanged || patch.isPending}
            >
              {patch.isPending ? "Deploying…" : missing ? "Deploy anyway" : "Deploy"}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}

// parseTarget reads the deploy field: a bare tag keeps the repository, a
// full reference may switch it.
export function parseTarget(raw: string, repository: string): { repository: string; tag: string; error?: string } {
  const v = raw.trim();
  if (!v) return { repository, tag: "", error: "Enter a tag" };
  if (v.includes("@")) return { repository, tag: "", error: "Digests aren't supported here; deploy a tag" };
  if (/^[\w][\w.-]{0,127}$/.test(v)) return { repository, tag: v };
  const ref = parseImageRef(v);
  if (!ref) return { repository, tag: "", error: "Not a valid tag or image reference" };
  if (!ref.tag) return { repository: ref.repository, tag: "", error: "Add a tag (e.g. :1.4.2)" };
  return { repository: ref.repository, tag: ref.tag };
}

function Explained({
  label,
  explain,
  children,
}: {
  label: string;
  explain: string;
  children: React.ReactElement<{ disabled?: boolean; className?: string; focusableWhenDisabled?: boolean }>;
}) {
  // A disabled button gets no hover or focus, so its explanation (often
  // "why you can't") would never show: keep it focusable and hoverable.
  const trigger = children.props.disabled
    ? cloneElement(children, {
        focusableWhenDisabled: true,
        className: cn(children.props.className, "aria-disabled:pointer-events-auto aria-disabled:cursor-not-allowed"),
      })
    : children;
  return (
    <Tooltip>
      <TooltipTrigger render={trigger} />
      <TooltipContent side="bottom" className="block max-w-64 text-left">
        <span className="block font-medium">{label}</span>
        <span className="mt-0.5 block opacity-80">{explain}</span>
      </TooltipContent>
    </Tooltip>
  );
}
