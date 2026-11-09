"use client";

import { smoothScroll } from "@/lib/reduced-motion";
import { lastDeployedAt } from "@/features/projects/facts";
import { useEffect, useMemo, useState } from "react";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { Skeleton } from "@/components/ui/skeleton";
import { useBuilds, useService } from "@/features/services";
import { useProject } from "@/features/projects";
import { useSetupStatus } from "@/features/github";
import { listServicePods, useServicePods } from "@/features/pods";
import { imageRolloutState, ROLLOUT_STEPS, type RolloutPod, type RolloutState } from "@/features/services/imageRollout";
import { useQuery } from "@tanstack/react-query";
import { CopyButton } from "@/components/ui/copy-button";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useCan, useCanOnProject, Perms } from "@/features/auth";
import {
  buildRefLabel,
  buildTrigger,
  classifyBuild,
  hasLiveInfo,
  liveBuildId,
  newerReleaseFailure,
  previousSucceeded,
  rollForwardTarget,
  type DeployBuild,
} from "@/features/builds";
import { envRevisionName, groupRevisionRuns, interleaveTimeline, useRevisions } from "@/features/revisions";
import type { KusoEnvironment } from "@/types/projects";
import { Check, ExternalLink, Loader2, Rocket, Undo2, X } from "lucide-react";
import { envGroupName } from "@/lib/env-group";
import { BuildRow } from "./BuildRow";
import { buildDurationLabel } from "./deployParts";
import { LiveDeploymentCard, type LiveHealth } from "./LiveDeploymentCard";
import { AutoDeployBanner, pushDeployState } from "./AutoDeployBanner";
import { RevisionGroup, RevisionRow } from "./RevisionRow";
import { ServicePodsSection } from "./ServicePodsSection";
import { ServiceEventsSection } from "./ServiceEventsSection";
import { ServiceAuditSection } from "./ServiceAuditSection";
import { useRedeploy } from "./ServiceHeaderActions";
import { FirstDeployProgress, FirstLiveCard } from "./FirstDeployProgress";
import { UptimeBadge } from "@/components/uptime/UptimeBadge";
import { useProjectUptime } from "@/features/uptime";

interface Props {
  project: string;
  service: string;
  env?: KusoEnvironment;
  // Public URL resolved by the overlay (custom domain → env host → …).
  url?: string;
  // Jumps to another overlay tab (and optionally a section in it).
  onNavigate?: (tab: string, anchor?: string) => void;
}

const HISTORY_PAGE = 20;

type HistoryFilter = "builds" | "all";
const FILTER_KEY = "kuso.deploy-history.filter";
const FILTER_OPTIONS = [
  { value: "builds", label: "Builds" },
  { value: "all", label: "All changes", title: "Builds plus config changes (variables, scale, settings)" },
] as const;

// useHistoryFilter remembers the Builds / All changes choice per browser.
function useHistoryFilter(): [HistoryFilter, (f: HistoryFilter) => void] {
  const [filter, setFilter] = useState<HistoryFilter>("all");
  useEffect(() => {
    try {
      if (window.localStorage.getItem(FILTER_KEY) === "builds") setFilter("builds");
    } catch {
      /* storage blocked: keep the default */
    }
  }, []);
  const set = (f: HistoryFilter) => {
    setFilter(f);
    try {
      window.localStorage.setItem(FILTER_KEY, f);
    } catch {
      /* storage blocked: the choice lasts for this view only */
    }
  };
  return [filter, set];
}

export function ServiceDeploymentsPanel({ project, service, env, url, onNavigate }: Props) {
  const builds = useBuilds(project, service);
  const redeploy = useRedeploy(project, service, env);
  const svc = useService(project, service);
  const proj = useProject(project);
  const gh = useSetupStatus();
  const isImage = svc.data?.spec.runtime === "image";
  const canDeploy = useCanOnProject(project, Perms.ServicesWrite);
  const canSeeClusterEvents = useCan(Perms.SettingsAdmin);
  const group = envGroupName(env);
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
          {!isImage && !canDeploy && (
            <span className="text-xs text-[var(--text-tertiary)]" title="Deploying needs editor access on this project">
              View only
            </span>
          )}
        </div>
      </div>

      {pushDeploy && !pushDeploy.on && <AutoDeployBanner state={pushDeploy} onNavigate={onNavigate} />}

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
          onDeploy={canDeploy ? redeploy.request : undefined}
          deploying={redeploy.pending}
          deployVerb={redeploy.verb}
          autoDeploy={!!pushDeploy?.on}
          onNavigate={onNavigate}
        />
      )}

      <ServicePodsSection project={project} service={service} envName={env?.metadata?.name} />
      {env ? (
        <ServiceEventsSection project={project} service={service} envName={env?.metadata?.name} canSeeClusterEvents={canSeeClusterEvents} />
      ) : null}
      <ServiceAuditSection project={project} service={service} />

      {redeploy.dialog}
    </div>
  );
}

function useLiveHealth(project: string, service: string, env?: KusoEnvironment, uptimeDown?: boolean): LiveHealth | undefined {
  const pods = useServicePods(project, service, env?.metadata?.name);
  const state = env?.status?.state;
  if (state === "crashlooping") return { label: "Crashlooping", tone: "bad" };
  if (state === "sleeping") return { label: "Sleeping", tone: "idle" };
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
  onDeploy,
  deploying,
  deployVerb = "Redeploy",
  autoDeploy,
  onNavigate,
}: {
  project: string;
  service: string;
  builds: DeployBuild[];
  env?: KusoEnvironment;
  url?: string;
  repoUrl?: string;
  canDeploy: boolean;
  uptimeDown?: boolean;
  // Starts a build; shown as the empty state's action when nothing was ever built.
  onDeploy?: () => void;
  deploying?: boolean;
  deployVerb?: string;
  autoDeploy?: boolean;
  onNavigate?: (tab: string, anchor?: string) => void;
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
  const releaseFailed = live ? newerReleaseFailure(builds, live) : undefined;
  const [filter, setFilter] = useHistoryFilter();

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

  // First deploy: nothing on any branch has ever succeeded and the newest
  // build is on its way, or the first success is still starting its pods.
  const succeededCount = builds.filter((b) => (b.status ?? "").toLowerCase() === "succeeded").length;
  const newestStatus = (newest?.status ?? "").toLowerCase();
  const firstDeploy: "queued" | "pending" | "running" | "start" | null =
    !newest
      ? null
      : succeededCount === 0 && ["queued", "pending", "running"].includes(newestStatus)
        ? (newestStatus as "queued" | "pending" | "running")
        : succeededCount === 1 && newestStatus === "succeeded" && (!live || podsComingUp(health))
          ? "start"
          : null;
  const firstLive =
    !firstDeploy && succeededCount === 1 && live && live.id === newest?.id && health?.tone === "ok" && !!url && recentlyFinished(live, 30);

  const history = visible.filter((b) => b.id !== liveId && !(firstDeploy && b.id === newest?.id));
  const revisions = [...(serviceRevs.data ?? []), ...(envRevs.data ?? [])];
  const timeline = groupRevisionRuns(interleaveTimeline(history, filter === "builds" ? [] : revisions));
  const liveIdx = live ? builds.indexOf(live) : -1;
  const shownTimeline = timeline.slice(0, limit);
  // Newest older build that can still go live again: its Rollback stays
  // visible. Builds newer than live are roll-forwards, not rollbacks.
  const rollbackTargetId = history.find(
    (b) => classifyBuild(b, liveId) === "superseded" && !!b.imageTag && (liveIdx < 0 || builds.indexOf(b) > liveIdx),
  )?.id;
  // Peek at the stored log of every failed build on screen for the step it
  // failed in and its first error line (paged, so at most HISTORY_PAGE).
  const peekIds = new Set(
    shownTimeline.flatMap((item) =>
      item.type === "build" && (item.build.status ?? "").toLowerCase() === "failed" ? [item.build.id] : [],
    ),
  );
  const showBuild = (id: string) => {
    setExpanded(id);
    requestAnimationFrame(() =>
      document.getElementById(`build-row-${id}`)?.scrollIntoView({ block: "start", behavior: smoothScroll() }),
    );
  };

  const firstDeployCard =
    firstDeploy && newest ? (
      <FirstDeployProgress
        project={project}
        service={service}
        build={newest}
        status={firstDeploy}
        starting={health && / ready$/.test(health.label) ? `Starting · ${health.label}` : undefined}
        repoUrl={repoUrl}
      />
    ) : null;

  const liveCard = firstDeployCard ? (
    firstDeployCard
  ) : live ? (
    <>
      {firstLive && url && <FirstLiveCard project={project} service={service} url={url} build={live} autoDeploy={autoDeploy} onNavigate={onNavigate} />}
      <LiveDeploymentCard
      project={project}
      service={service}
      envGroup={group}
      build={live}
      previous={previousSucceeded(builds, live)}
      rolledBackFrom={rolledBackFrom}
      repoUrl={repoUrl}
      url={url}
      deployedAt={lastDeployedAt(env)}
      health={health}
      serving={!["stopped", "sleeping"].includes(env?.status?.state ?? "")}
      canDeploy={canDeploy}
      releaseFailed={releaseFailed}
      onShowBuild={showBuild}
      trigger={buildTrigger(live, builds)}
    />
    </>
  ) : visible.length > 0 ? (
    <div className="rounded-lg border border-dashed border-[var(--border-default)] px-4 py-3 text-sm text-[var(--text-secondary)]">
      Nothing is live in <strong className="text-[var(--text-primary)]">{group}</strong> yet.{" "}
      {newestStatus === "failed"
        ? autoDeploy
          ? `The latest build failed: fix the cause shown below and push, or press ${deployVerb} to try the same commit again.`
          : `The latest build failed: fix the cause shown below, then press ${deployVerb}. Auto-deploy is off, so pushes won't retry it.`
        : autoDeploy
          ? "The first successful build below goes live automatically."
          : "The first successful build goes live. Auto-deploy is off, so press Deploy to start one."}
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
              ? `This service has ${builds.length} build${builds.length === 1 ? "" : "s"} on ${otherBranches.slice(0, 3).join(", ")}${otherBranches.length > 3 ? ` and ${otherBranches.length - 3} more` : ""}. Push to ${envBranch} or click ${deployVerb}.`
              : "Nothing has been built yet. Add any variables the app needs, then deploy to build and ship the first version."}
          </p>
          {onDeploy && builds.length === 0 && (
            <Button size="sm" className="mt-4" onClick={onDeploy} disabled={deploying}>
              <Rocket className="h-3.5 w-3.5" />
              {deploying ? "Starting…" : deployVerb}
            </Button>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {liveCard}
      {(history.length > 0 || revisions.length > 0) && (
        <section aria-labelledby="deploy-history-h" className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h4 id="deploy-history-h" className="text-xs font-medium text-[var(--text-secondary)]">
              History
              <span className="ml-1.5 font-normal text-[var(--text-tertiary)]">
                {history.length} build{history.length === 1 ? "" : "s"}
                {filter === "all" && revisions.length > 0
                  ? ` · ${revisions.length} config change${revisions.length === 1 ? "" : "s"}`
                  : ""}
              </span>
            </h4>
            {revisions.length > 0 && (
              <Segmented size="sm" label="Show in history" options={FILTER_OPTIONS} value={filter} onChange={setFilter} />
            )}
          </div>
          {/* overflow-hidden trims the config-change rail where the list ends */}
          <ul className="space-y-2 overflow-hidden">
            {live && rolledBackFrom && (
              <RollbackMarker live={live} from={rolledBackFrom} at={lastDeployedAt(env)} onShowBuild={showBuild} />
            )}
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
                  liveBuild={live}
                  peekLog={peekIds.has(b.id)}
                  repoUrl={repoUrl}
                  status={s}
                  duration={s === "running" ? "" : buildDurationLabel(b)}
                  runningSince={s === "running" ? b.startedAt : undefined}
                  isOpen={expanded === b.id}
                  canDeploy={canDeploy}
                  pinActions={b.id === rollbackTargetId}
                  trigger={buildTrigger(b, builds)}
                  onToggle={() => setExpanded(expanded === b.id ? null : b.id)}
                  onNavigate={onNavigate}
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
      {live && !firstDeploy && !rolledBackFrom && history.length === 0 && (
        <p className="text-xs text-[var(--text-tertiary)]">
          {buildRefLabel(live, 7)} is the only deployment so far.
        </p>
      )}
    </div>
  );
}

// RollbackMarker pins the rollback itself at the top of the history: it
// isn't a build, so it would otherwise only show on the live card.
function RollbackMarker({
  live,
  from,
  at,
  onShowBuild,
}: {
  live: DeployBuild;
  from: DeployBuild;
  at?: string;
  onShowBuild: (id: string) => void;
}) {
  const fromSha = buildRefLabel(from, 7) || from.id;
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-[var(--border-default)] bg-[var(--bg-tertiary)] px-3 py-2 text-xs text-[var(--text-secondary)] sm:px-4">
      <Undo2 aria-hidden className="h-4 w-4 shrink-0 text-[var(--text-secondary)]" />
      <span className="min-w-0 flex-1">
        <span className="font-medium text-[var(--text-primary)]">
          Rolled back to <span className="font-mono">{buildRefLabel(live, 7) || live.id}</span>
        </span>{" "}
        from{" "}
        <button
          type="button"
          onClick={() => onShowBuild(from.id)}
          className="font-mono text-[var(--text-primary)] underline-offset-2 hover:underline"
        >
          {fromSha}
        </button>
        {at && (
          <>
            {" · "}
            <time dateTime={at} title={new Date(at).toLocaleString()}>
              {relativeTime(at)}
            </time>
          </>
        )}
      </span>
      <span className="rounded border border-[var(--border-default)] px-1.5 py-px text-[var(--text-secondary)]">Pinned</span>
    </li>
  );
}

// podsComingUp: the first pods exist but aren't all ready yet (or none
// have been created). A crashloop is labelled as such and isn't this.
function podsComingUp(h?: LiveHealth): boolean {
  if (!h) return false;
  return h.label === "No running pods" || (h.tone !== "ok" && / ready$/.test(h.label));
}

function recentlyFinished(b: DeployBuild, minutes: number): boolean {
  const t = Date.parse(b.finishedAt ?? "");
  return Number.isFinite(t) && Date.now() - t < minutes * 60_000;
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
                "mt-1.5 flex items-start gap-1 text-xs leading-tight",
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
