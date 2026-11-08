"use client";

import { useEffect, useMemo, useState } from "react";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { buildTriggerMessage, useBuilds, useService, useTriggerBuild } from "@/features/services";
import { useProject } from "@/features/projects";
import { useSetupStatus } from "@/features/github";
import { listServicePods, useServicePods } from "@/features/pods";
import { imageRolloutState, ROLLOUT_STEPS, type RolloutPod, type RolloutState } from "@/features/services/imageRollout";
import { useQuery } from "@tanstack/react-query";
import { CopyButton } from "@/components/ui/copy-button";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useCanOnProject, Perms } from "@/features/auth";
import {
  buildRefLabel,
  classifyBuild,
  hasLiveInfo,
  liveBuildId,
  previousSucceeded,
  rollForwardTarget,
  type DeployBuild,
} from "@/features/builds";
import { envRevisionName, groupRevisionRuns, interleaveTimeline, useRevisions } from "@/features/revisions";
import type { KusoEnvironment } from "@/types/projects";
import { Check, ExternalLink, Loader2, Rocket, X } from "lucide-react";
import { toast } from "sonner";
import { envGroupName, isProductionGroup } from "@/lib/env-group";
import { BuildRow } from "./BuildRow";
import { buildDurationLabel } from "./deployParts";
import { LiveDeploymentCard, type LiveHealth } from "./LiveDeploymentCard";
import { AutoDeployBanner, pushDeployState } from "./AutoDeployBanner";
import { RevisionGroup, RevisionRow } from "./RevisionRow";
import { ServicePodsSection } from "./ServicePodsSection";
import { ServiceAuditSection } from "./ServiceAuditSection";
import { UptimeBadge } from "@/components/uptime/UptimeBadge";
import { useProjectUptime } from "@/features/uptime";

interface Props {
  project: string;
  service: string;
  env?: KusoEnvironment;
  // Public URL resolved by the overlay (custom domain → env host → …).
  url?: string;
}

const HISTORY_PAGE = 20;

export function ServiceDeploymentsPanel({ project, service, env, url }: Props) {
  const builds = useBuilds(project, service);
  const trigger = useTriggerBuild(project, service);
  const svc = useService(project, service);
  const proj = useProject(project);
  const gh = useSetupStatus();
  const isImage = svc.data?.spec.runtime === "image";
  const canDeploy = useCanOnProject(project, Perms.ServicesWrite);
  const [confirmRedeploy, setConfirmRedeploy] = useState(false);
  const group = envGroupName(env);
  const branch = env?.spec?.branch || svc.data?.spec.repo?.defaultBranch || proj.data?.project.spec.defaultRepo?.defaultBranch;
  const repoUrl = svc.data?.spec.repo?.url || proj.data?.project.spec.defaultRepo?.url;

  const uptime = useProjectUptime(project);
  const uptimeStatus =
    group === "production" ? (uptime.data?.services ?? []).find((s) => s.service === service) : undefined;

  const pushDeploy = pushDeployState({
    spec: svc.data?.spec,
    projectRepoUrl: proj.data?.project.spec.defaultRepo?.url,
    envBranch: env?.spec?.branch,
    githubConfigured: gh.isSuccess ? gh.data.configured : undefined,
  });

  const redeployBody = env?.spec?.branch ? { branch: env.spec.branch } : {};
  const onRedeploy = async () => {
    try {
      const res = await trigger.mutateAsync(redeployBody);
      toast.success(buildTriggerMessage(res, "Redeploy started"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to trigger build");
    }
  };
  // A production redeploy is a real build + rollout; confirm it. Other env
  // groups are cheap and expected, so they fire straight away.
  const requestRedeploy = () => {
    if (env && isProductionGroup(env)) setConfirmRedeploy(true);
    else void onRedeploy();
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h3 className="text-sm font-semibold text-[var(--text-primary)]">
            Deployments <span className="font-normal text-[var(--text-secondary)]">· {group}</span>
          </h3>
          {pushDeploy?.on && <AutoDeployBanner state={pushDeploy} />}
        </div>
        <div className="flex items-center gap-2">
          {uptimeStatus && <UptimeBadge status={uptimeStatus} />}
          {isImage ? null : canDeploy ? (
            <Button size="sm" onClick={requestRedeploy} disabled={trigger.isPending}>
              <Rocket className="h-3.5 w-3.5" />
              {trigger.isPending ? "Starting…" : "Redeploy"}
            </Button>
          ) : (
            <span className="text-xs text-[var(--text-tertiary)]" title="Deploying needs editor access on this project">
              View only
            </span>
          )}
        </div>
      </div>

      {pushDeploy && !pushDeploy.on && <AutoDeployBanner state={pushDeploy} />}

      {isImage ? (
        <ImageDeploymentCard
          project={project}
          service={service}
          env={env}
          url={url}
          image={svc.data?.spec.image}
        />
      ) : builds.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-36 w-full rounded-lg" />
          <Skeleton className="h-16 w-full rounded-lg" />
          <Skeleton className="h-16 w-full rounded-lg" />
        </div>
      ) : builds.isError ? (
        <QueryErrorState what="builds" error={builds.error} onRetry={() => void builds.refetch()} />
      ) : (
        <DeployList
          project={project}
          service={service}
          builds={builds.data ?? []}
          env={env}
          url={url}
          repoUrl={repoUrl}
          canDeploy={canDeploy}
          uptimeDown={uptimeStatus?.state === "down"}
        />
      )}

      <ServicePodsSection project={project} service={service} envName={env?.metadata?.name} />
      <ServiceAuditSection project={project} service={service} />

      <ConfirmDialog
        open={confirmRedeploy}
        title={`Redeploy ${group}?`}
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
          setConfirmRedeploy(false);
          void onRedeploy();
        }}
        onCancel={() => setConfirmRedeploy(false)}
      />
    </div>
  );
}

function useLiveHealth(project: string, service: string, env?: KusoEnvironment, uptimeDown?: boolean): LiveHealth | undefined {
  const pods = useServicePods(project, service, env?.metadata?.name);
  const state = env?.status?.state;
  if (state === "crashlooping") return { label: "Crashlooping", tone: "bad" };
  if (state === "sleeping") return { label: "Asleep", tone: "idle" };
  if (state === "stopped") return { label: "Stopped", tone: "idle" };
  if (uptimeDown) return { label: "Not responding", tone: "bad" };
  const list = pods.data?.pods;
  if (!list) return undefined;
  if (list.length === 0) return { label: "No running pods", tone: "idle" };
  const ready = list.filter((p) => p.ready).length;
  const label = `${ready}/${list.length} ${list.length === 1 ? "instance" : "instances"} ready`;
  return { label, tone: ready === list.length ? "ok" : ready === 0 ? "bad" : "warn" };
}

// DeployList renders the live deployment, then this env's history (builds on
// its branch interleaved with config revisions), newest first.
function DeployList({
  project,
  service,
  builds,
  env,
  url,
  repoUrl,
  canDeploy,
  uptimeDown,
}: {
  project: string;
  service: string;
  builds: DeployBuild[];
  env?: KusoEnvironment;
  url?: string;
  repoUrl?: string;
  canDeploy: boolean;
  uptimeDown?: boolean;
}) {
  const group = envGroupName(env);
  const envCRName = env?.metadata?.name;
  const serviceRevs = useRevisions(project, "service", service);
  const envRevs = useRevisions(project, "environment", envCRName ? envRevisionName(project, envCRName) : undefined);
  const health = useLiveHealth(project, service, env, uptimeDown);

  const envImage = (env?.spec as { image?: { tag?: string } } | undefined)?.image;
  // Only builds of this env's branch belong in its history; a PR build
  // would otherwise show up under production.
  const envBranch = env?.spec?.branch;
  const visible = useMemo(
    () => (envBranch ? builds.filter((b) => (b.branch ?? "") === envBranch) : builds),
    [builds, envBranch],
  );
  // liveEnvs is authoritative across branches (a forced rollback can leave
  // the env on another branch's image); old servers fall back to a guess.
  const liveId = liveBuildId(hasLiveInfo(builds) ? builds : visible, group, envImage?.tag);
  const live = builds.find((b) => b.id === liveId);
  const rolledBackFrom = live ? rollForwardTarget(builds, live) : undefined;

  // A failed or in-progress newest build is the thing to look at: open it
  // (once per build, so collapsing it sticks).
  const newest = visible[0];
  const newestFailed =
    !!newest && ["failed", "release-failed", "running"].includes((newest.status ?? "").toLowerCase());
  const [expanded, setExpanded] = useState<string | null>(null);
  const [autoOpened, setAutoOpened] = useState<string | null>(null);
  useEffect(() => {
    if (newestFailed && newest && autoOpened !== newest.id) {
      setExpanded(newest.id);
      setAutoOpened(newest.id);
    }
  }, [newestFailed, newest, autoOpened]);

  const [limit, setLimit] = useState(HISTORY_PAGE);

  const history = visible.filter((b) => b.id !== liveId);
  const timeline = groupRevisionRuns(
    interleaveTimeline(history, [...(serviceRevs.data ?? []), ...(envRevs.data ?? [])]),
  );
  const liveIdx = live ? builds.indexOf(live) : -1;
  const shownTimeline = timeline.slice(0, limit);

  const liveCard = live ? (
    <LiveDeploymentCard
      project={project}
      service={service}
      envGroup={group}
      build={live}
      previous={previousSucceeded(builds, live)}
      rolledBackFrom={rolledBackFrom}
      repoUrl={repoUrl}
      url={url}
      deployedAt={env?.status?.lastDeployedAt}
      health={health}
      serving={!["stopped", "sleeping"].includes(env?.status?.state ?? "")}
      canDeploy={canDeploy}
    />
  ) : visible.length > 0 ? (
    <div className="rounded-lg border border-dashed border-[var(--border-default)] px-4 py-3 text-sm text-[var(--text-secondary)]">
      Nothing is live in <strong className="text-[var(--text-primary)]">{group}</strong> yet. The first successful build
      below goes live automatically.
    </div>
  ) : null;

  if (visible.length === 0) {
    const otherBranches = Array.from(new Set(builds.map((b) => b.branch ?? "—"))).filter((b) => b !== envBranch);
    return (
      <div className="space-y-3">
        {liveCard}
        <div className="rounded-lg border border-dashed border-[var(--border-default)] px-6 py-8 text-center">
          <p className="text-sm font-medium text-[var(--text-primary)]">
            {envBranch && builds.length > 0 ? (
              <>
                No builds on <span className="font-mono">{envBranch}</span> yet
              </>
            ) : (
              "No deployments yet"
            )}
          </p>
          <p className="mx-auto mt-1 max-w-sm text-xs leading-relaxed text-[var(--text-secondary)]">
            {envBranch && builds.length > 0
              ? `This service has ${builds.length} build${builds.length === 1 ? "" : "s"} on ${otherBranches.slice(0, 3).join(", ")}${otherBranches.length > 3 ? ` and ${otherBranches.length - 3} more` : ""}. Push to ${envBranch} or click Redeploy.`
              : "Push to the connected branch or click Redeploy to build and ship the first version."}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {liveCard}
      {timeline.length > 0 && (
        <section aria-labelledby="deploy-history-h" className="space-y-2">
          <h4 id="deploy-history-h" className="flex items-baseline justify-between text-xs font-medium text-[var(--text-secondary)]">
            <span>History</span>
            <span className="font-normal text-[var(--text-tertiary)]">
              {history.length} build{history.length === 1 ? "" : "s"}
            </span>
          </h4>
          <ul className="space-y-2">
            {shownTimeline.map((item) => {
              if (item.type === "revisions") {
                return (
                  <RevisionGroup
                    key={`revs-${item.revisions[0].id}`}
                    project={project}
                    revisions={item.revisions}
                    canRevert={canDeploy}
                  />
                );
              }
              if (item.type === "revision") {
                return (
                  <RevisionRow
                    key={`rev-${item.revision.id}`}
                    project={project}
                    revision={item.revision}
                    canRevert={canDeploy}
                  />
                );
              }
              const b = item.build;
              const s = classifyBuild(b, liveId);
              return (
                <BuildRow
                  key={b.id}
                  project={project}
                  service={service}
                  env={group}
                  build={b}
                  previous={previousSucceeded(builds, b)}
                  newerThanLive={liveIdx >= 0 && builds.indexOf(b) < liveIdx}
                  repoUrl={repoUrl}
                  status={s}
                  duration={s === "running" ? "" : buildDurationLabel(b)}
                  runningSince={s === "running" ? b.startedAt : undefined}
                  isOpen={expanded === b.id}
                  canDeploy={canDeploy}
                  onToggle={() => setExpanded(expanded === b.id ? null : b.id)}
                />
              );
            })}
          </ul>
          {timeline.length > limit && (
            <button
              type="button"
              onClick={() => setLimit((l) => l + HISTORY_PAGE)}
              className="w-full rounded-lg border border-[var(--border-subtle)] py-2 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-secondary)] hover:text-[var(--text-primary)]"
            >
              Show {Math.min(HISTORY_PAGE, timeline.length - limit)} more of {timeline.length - limit}
            </button>
          )}
        </section>
      )}
      {live && !rolledBackFrom && history.length === 0 && (
        <p className="text-xs text-[var(--text-tertiary)]">
          {buildRefLabel(live, 7)} is the only deployment so far.
        </p>
      )}
    </div>
  );
}

// ImageDeploymentCard replaces the build history for services that deploy a
// registry image: there are no builds, so show the rollout itself
// (scheduled → image pulled → started → ready) from env state + pods.
function ImageDeploymentCard({
  project,
  service,
  env,
  url,
  image,
}: {
  project: string;
  service: string;
  env?: KusoEnvironment;
  url?: string;
  image?: { repository?: string; tag?: string };
}) {
  const envName = env?.metadata?.name;
  const envState = env?.status?.state;
  const group = envGroupName(env);
  // Same cache entry as useServicePods; polls faster until the rollout settles.
  const pods = useQuery({
    queryKey: ["pods", "service", project, service, envName ?? ""],
    queryFn: () => listServicePods(project, service, envName ?? ""),
    enabled: !!envName,
    refetchInterval: (q) => {
      const st = imageRolloutState(envState, (q.state.data?.pods ?? undefined) as RolloutPod[] | undefined);
      return st && st.status === "active" ? 3_000 : 15_000;
    },
  });
  const podList = pods.data ? ((pods.data.pods ?? []) as RolloutPod[]) : undefined;
  const state = imageRolloutState(envState, podList);
  const ref = image?.repository ? `${image.repository}:${image.tag || "latest"}` : undefined;
  const deployedAt = env?.status?.lastDeployedAt;

  if (!state) {
    return pods.isError ? (
      <QueryErrorState what="pods" error={pods.error} onRetry={() => void pods.refetch()} />
    ) : (
      <Skeleton className="h-36 w-full rounded-lg" />
    );
  }
  const live = state.status === "done";
  const tone =
    state.status === "failed"
      ? "border-[var(--danger)]/45"
      : live
        ? "border-[var(--success)]/35"
        : "border-[var(--border-default)]";

  return (
    <section aria-label={`Deployment in ${group}`} className={cn("rounded-lg border bg-[var(--bg-secondary)] p-4", tone)}>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <RolloutBadge state={state} />
        <span className="text-[var(--text-secondary)]">in {group}</span>
        {state.total > 0 && (
          <span className="rounded-full border border-[var(--border-subtle)] px-2 py-0.5 text-[var(--text-secondary)]">
            {state.ready}/{state.total} {state.total === 1 ? "instance" : "instances"} ready
          </span>
        )}
        {live && deployedAt && (
          <time dateTime={deployedAt} title={new Date(deployedAt).toLocaleString()} className="text-[var(--text-tertiary)]">
            deployed {relativeTime(deployedAt)}
          </time>
        )}
      </div>

      <p className="mt-3 text-base font-semibold text-[var(--text-primary)]">{state.title}</p>
      {state.detail && <p className="mt-0.5 break-words text-sm text-[var(--text-secondary)]">{state.detail}</p>}

      {ref && (
        <div className="mt-3 flex min-w-0 items-center gap-1 text-xs text-[var(--text-secondary)]">
          <span className="shrink-0">Image</span>
          <span className="min-w-0 truncate font-mono text-[var(--text-primary)]" title={ref}>
            {ref}
          </span>
          <CopyButton value={ref} label="Copy image reference" size="xs" />
        </div>
      )}

      {(state.status === "active" || state.status === "failed") && <RolloutSteps state={state} />}

      {live && url && (
        <div className="mt-3 flex min-w-0 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] py-1 pl-2.5 pr-1">
          <a href={url} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--text-primary)] hover:underline">
            {url.replace(/^https?:\/\//, "")}
          </a>
          <CopyButton value={url} label="Copy URL" size="xs" />
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            aria-label="Open in new tab"
            className="inline-flex h-6 w-6 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <ExternalLink className="h-3.5 w-3.5" />
          </a>
        </div>
      )}
    </section>
  );
}

function RolloutBadge({ state }: { state: RolloutState }) {
  if (state.status === "done") {
    return (
      <span className="inline-flex items-center gap-1.5 font-medium text-[var(--success)]">
        <span className="h-2 w-2 rounded-full bg-[var(--success)]" />
        Live
      </span>
    );
  }
  if (state.status === "failed") {
    return (
      <span className="inline-flex items-center gap-1.5 font-medium text-[var(--danger)]">
        <span className="h-2 w-2 rounded-full bg-[var(--danger)]" />
        Rollout stuck
      </span>
    );
  }
  if (state.status === "idle") {
    return (
      <span className="inline-flex items-center gap-1.5 font-medium text-[var(--text-primary)]">
        <span className="h-2 w-2 rounded-full bg-[var(--text-tertiary)]" />
        {state.title}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 font-medium text-[var(--info)]">
      <Loader2 className="h-3 w-3 motion-safe:animate-spin" aria-hidden />
      Rolling out
    </span>
  );
}

function RolloutSteps({ state }: { state: RolloutState }) {
  const current = ROLLOUT_STEPS.findIndex((s) => s.step === state.step);
  return (
    <ol className="mt-4 grid grid-cols-4 gap-1.5 sm:gap-2" aria-label="Rollout progress">
      {ROLLOUT_STEPS.map((s, i) => {
        const done = i < current || (i === current && state.status === "done");
        const failed = i === current && state.status === "failed";
        const active = i === current && state.status === "active";
        return (
          <li key={s.step} className="min-w-0" aria-current={i === current ? "step" : undefined}>
            <div
              className={cn(
                "h-1 rounded-full",
                done ? "bg-[var(--success)]" : failed ? "bg-[var(--danger)]" : active ? "bg-[var(--info)]" : "bg-[var(--border-default)]",
              )}
            />
            <div
              className={cn(
                "mt-1.5 flex items-start gap-1 text-2xs leading-tight sm:text-xs",
                done || active || failed ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]",
              )}
            >
              {done ? (
                <Check className="mt-px h-3 w-3 shrink-0 text-[var(--success)]" aria-hidden />
              ) : failed ? (
                <X className="h-3 w-3 shrink-0 text-[var(--danger)]" aria-hidden />
              ) : active ? (
                <Loader2 className="h-3 w-3 shrink-0 text-[var(--info)] motion-safe:animate-spin" aria-hidden />
              ) : null}
              <span className="min-w-0">{s.label}</span>
            </div>
          </li>
        );
      })}
    </ol>
  );
}
