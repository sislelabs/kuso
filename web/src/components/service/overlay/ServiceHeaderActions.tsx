"use client";

import { cloneElement, useState } from "react";
import Link from "next/link";
import { BellPlus, MoreHorizontal, Play, Rocket, RotateCw, Square } from "lucide-react";
import { toast } from "sonner";
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
  useTriggerBuild,
} from "@/features/services";
import { useProject } from "@/features/projects";
import { useCan, useCanOnProject, Perms } from "@/features/auth";
import { envGroupName, isProductionGroup } from "@/lib/env-group";
import { cn } from "@/lib/utils";
import type { KusoEnvironment } from "@/types/projects";

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
  stop: "Scales the service to zero. Visitors get a 503 until you start it again.",
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
  const neverBuilt = !!builds.data && builds.data.length === 0;
  const verb = neverBuilt ? "Deploy" : "Redeploy";

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

// ServiceHeaderActions is the overlay header's action row: Redeploy (the
// primary), Restart and Stop/Start. Each explains itself in a tooltip.
// Below 640px only Redeploy stays inline; Restart and Stop move into an
// overflow menu so a mis-tap can't take the service down.
export function ServiceHeaderActions({
  project,
  service,
  env,
  stopped,
  showRestart,
}: {
  project: string;
  service: string;
  env?: KusoEnvironment;
  stopped: boolean;
  showRestart: boolean;
}) {
  const canWrite = useCanOnProject(project, Perms.ServicesWrite);
  const redeploy = useRedeploy(project, service, env);
  const restart = useRestartService(project, service);
  const stop = useStopService(project, service);
  const start = useStartService(project, service);
  const [confirm, setConfirm] = useState<"restart" | "stop" | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const group = envGroupName(env);
  const canAlert = useCan(Perms.SettingsAdmin);

  const onRestart = async () => {
    try {
      await restart.mutateAsync(group);
      toast.success(`Restarting ${service} (${group})`);
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

  const restartBtn = showRestart && !stopped;
  // A service that has never had an image has no pods to restart or stop.
  const neverRan = env?.status?.state === "no_image";
  const NEVER_RAN = "Nothing is running yet: this service has never been deployed.";
  const restartExplain = !canWrite ? NO_ACCESS : neverRan ? NEVER_RAN : EXPLAIN.restart(group);
  const stopExplain = !canWrite ? NO_ACCESS : neverRan ? NEVER_RAN : EXPLAIN.stop;
  const menuItem =
    "flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left hover:bg-[var(--bg-tertiary)] disabled:opacity-50";

  return (
    <>
      {!redeploy.isImage && (
        <Explained label={redeploy.verb} explain={canWrite ? redeploy.explain : NO_ACCESS}>
          <Button size="sm" onClick={redeploy.request} disabled={!canWrite || redeploy.pending} className="h-7 px-2.5 text-xs">
            <Rocket className="h-3 w-3" />
            {redeploy.pending ? "Starting…" : redeploy.verb}
          </Button>
        </Explained>
      )}

      {/* ≥640px: inline secondary actions, Stop last and visually quiet. */}
      <div className="hidden items-center gap-1.5 sm:flex">
        {restartBtn && (
          <Explained label="Restart" explain={restartExplain}>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setConfirm("restart")}
              disabled={!canWrite || neverRan || restart.isPending}
              className="h-7 px-2.5 text-xs"
            >
              <RotateCw className={cn("h-3 w-3", restart.isPending && "motion-safe:animate-spin")} />
              Restart
            </Button>
          </Explained>
        )}
        {stopped ? (
          <Explained label="Start" explain={canWrite ? EXPLAIN.start : NO_ACCESS}>
            <Button size="sm" variant="secondary" onClick={onStart} disabled={!canWrite || start.isPending} className="h-7 px-2.5 text-xs">
              <Play className="h-3 w-3" />
              {start.isPending ? "Starting…" : "Start"}
            </Button>
          </Explained>
        ) : (
          <Explained label="Stop" explain={stopExplain}>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setConfirm("stop")}
              disabled={!canWrite || neverRan || stop.isPending}
              className="h-7 px-2.5 text-xs hover:text-[var(--danger)]"
            >
              <Square className="h-3 w-3" />
              Stop
            </Button>
          </Explained>
        )}
      </div>

      {/* <640px: Start stays inline (it's the way back) and Restart/Stop
          fold into the ⋯ menu. The menu itself shows at every width: it
          also carries the per-service alert shortcuts. */}
      <div className="flex items-center gap-1.5">
        {stopped && (
          <Button size="sm" variant="secondary" onClick={onStart} disabled={!canWrite || start.isPending} className="h-7 px-2.5 text-xs sm:hidden">
            <Play className="h-3 w-3" />
            {start.isPending ? "Starting…" : "Start"}
          </Button>
        )}
        {(restartBtn || !stopped || canAlert) && (
          <Popover open={menuOpen} onOpenChange={setMenuOpen}>
            <PopoverTrigger
              aria-label="More service actions"
              className={cn(
                "inline-flex h-7 w-8 items-center justify-center rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                !canAlert && "sm:hidden",
              )}
            >
              <MoreHorizontal className="h-4 w-4" />
            </PopoverTrigger>
            <PopoverContent align="start" className="w-72 gap-0.5 rounded-lg p-1">
              {restartBtn && (
                <button
                  type="button"
                  disabled={!canWrite || neverRan}
                  className={cn(menuItem, "sm:hidden")}
                  onClick={() => {
                    setMenuOpen(false);
                    setConfirm("restart");
                  }}
                >
                  <RotateCw className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--text-secondary)]" />
                  <span className="min-w-0">
                    <span className="block text-sm text-[var(--text-primary)]">Restart</span>
                    <span className="block text-xs text-[var(--text-secondary)]">
                      {restartExplain}
                    </span>
                  </span>
                </button>
              )}
              {!stopped && (
                <button
                  type="button"
                  disabled={!canWrite || neverRan}
                  className={cn(menuItem, "sm:hidden")}
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
                <div role="group" aria-labelledby="svc-alert-menu-heading" className={cn((restartBtn || !stopped) && "border-t border-[var(--border-subtle)] pt-1 sm:border-t-0 sm:pt-0")}>
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
        body={EXPLAIN.stop}
        confirmLabel="Stop service"
        destructive
        pending={stop.isPending}
        onConfirm={onStop}
        onCancel={() => setConfirm(null)}
      />
    </>
  );
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
