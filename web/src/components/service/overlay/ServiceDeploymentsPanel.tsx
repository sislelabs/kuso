"use client";

import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { buildTriggerMessage, useBuilds, useService, useTriggerBuild } from "@/features/services";
import { useCanOnProject, Perms } from "@/features/auth";
import type { BuildSummary } from "@/features/services/api";
import {
  classifyBuild,
  hasLiveInfo,
  isRolledBack,
  liveBuildId,
  type DeployBuild,
} from "@/features/builds";
import { envRevisionName, interleaveTimeline, useRevisions } from "@/features/revisions";
import type { KusoEnvironment } from "@/types/projects";
import { RotateCcw, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import { envGroupName, isProductionGroup } from "@/lib/env-group";
import { BuildRow, type BuildRowStatus } from "./BuildRow";
import { LiveNowLine } from "./LiveNowLine";
import { RevisionRow } from "./RevisionRow";

interface Props {
  project: string;
  service: string;
  env?: KusoEnvironment;
}

// formatDuration turns a millisecond span into the kind of label the
// build CI/CDs of the world print: "12s", "1m 04s", "3m 17s",
// "1h 02m". Sub-second spans floor to "0s" rather than disappear so
// a freshly-clicked redeploy shows a live counter immediately.
function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (min < 60) {
    return remSec === 0 ? `${min}m` : `${min}m ${String(remSec).padStart(2, "0")}s`;
  }
  const hr = Math.floor(min / 60);
  const remMin = min % 60;
  return remMin === 0 ? `${hr}h` : `${hr}h ${String(remMin).padStart(2, "0")}m`;
}

// buildDuration returns the time-on-task for a build:
//   - running:   now - startedAt (live counter)
//   - finished:  finishedAt - startedAt
//   - missing:   "" so the renderer skips the whole pill
function buildDuration(b: BuildSummary, status: BuildRowStatus): string {
  const startMs = b.startedAt ? Date.parse(b.startedAt) : NaN;
  if (!Number.isFinite(startMs)) return "";
  if (status === "running") return formatDuration(Date.now() - startMs);
  const endMs = b.finishedAt ? Date.parse(b.finishedAt) : NaN;
  if (!Number.isFinite(endMs)) return "";
  return formatDuration(endMs - startMs);
}

// useNowTick re-renders every second while `running` is true so the
// live duration display ticks. Returns nothing — the side effect is
// the bumped state. Stops the interval when nothing is running so a
// quiet panel doesn't burn cycles forcing renders.
function useNowTick(running: boolean) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [running]);
}

export function ServiceDeploymentsPanel({ project, service, env }: Props) {
  const builds = useBuilds(project, service);
  const trigger = useTriggerBuild(project, service);
  // runtime=image services never build — the server 400s the build
  // endpoint for them ("change image.tag and save the service spec to
  // redeploy"). Hide the Redeploy button and point at Settings instead.
  const svc = useService(project, service);
  const isImage = svc.data?.spec.runtime === "image";
  const [expanded, setExpanded] = useState<string | null>(null);
  const canDeploy = useCanOnProject(project, Perms.ServicesWrite);
  // Re-render every second while at least one build is running so
  // the in-flight duration display ticks visibly.
  const anyRunning = (builds.data ?? []).some(
    (b) => (b.status ?? "").toLowerCase() === "running",
  );
  useNowTick(anyRunning);

  const [confirmRedeploy, setConfirmRedeploy] = useState(false);

  const onRedeploy = async (body: { branch?: string; ref?: string } = {}) => {
    try {
      const res = await trigger.mutateAsync(body);
      toast.success(buildTriggerMessage(res, "Redeploy started"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to trigger build");
    }
  };

  const redeployBody = env?.spec?.branch ? { branch: env.spec.branch } : {};
  const group = envGroupName(env);
  // Redeploy is a real production build+rollout on one click. Confirm
  // for the production env (a misclick rebuilds live); preview/staging
  // redeploys fire straight through — they're cheap and expected.
  const requestRedeploy = () => {
    if (env && isProductionGroup(env)) {
      setConfirmRedeploy(true);
      return;
    }
    onRedeploy(redeployBody);
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3 text-xs text-[var(--text-secondary)]">
          {env?.status?.url ? (
            <a
              href={env.status.url as string}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 font-mono text-[var(--accent)] hover:underline"
            >
              {(env.status.url as string).replace(/^https?:\/\//, "")}
              <ExternalLink className="h-3 w-3" />
            </a>
          ) : (
            <span className="font-mono text-[var(--text-tertiary)]">no URL yet</span>
          )}
          {env && (
            <span className="font-mono text-[var(--text-tertiary)]">{group}</span>
          )}
        </div>
        {isImage ? (
          <span
            className="font-mono text-[10px] text-[var(--text-tertiary)]"
            title="This service deploys a pre-built image and never builds. To roll it, change the image tag in Settings → Image and save."
          >
            pre-built image — redeploy via Settings
          </span>
        ) : canDeploy ? (
          <Button
            size="sm"
            onClick={requestRedeploy}
            disabled={trigger.isPending}
          >
            <RotateCcw className="h-3.5 w-3.5" />
            {trigger.isPending ? "Triggering…" : "Redeploy"}
          </Button>
        ) : (
          <span
            className="font-mono text-[10px] text-[var(--text-tertiary)]"
            title="services:write permission required"
          >
            read-only
          </span>
        )}
      </div>

      {builds.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : builds.isError ? (
        <QueryErrorState what="builds" error={builds.error} onRetry={() => void builds.refetch()} />
      ) : (
        <BuildsList
          project={project}
          service={service}
          builds={builds.data ?? []}
          env={env}
          expanded={expanded}
          setExpanded={setExpanded}
          canDeploy={canDeploy}
        />
      )}

      <ConfirmDialog
        open={confirmRedeploy}
        title={`Redeploy ${group}?`}
        destructive={false}
        confirmLabel="Redeploy"
        body={
          <span>
            This triggers a fresh build of{" "}
            <span className="font-mono text-[var(--text-primary)]">{service}</span>{" "}
            from{" "}
            <span className="font-mono text-[var(--text-primary)]">
              {env?.spec?.branch || "the default branch"}
            </span>{" "}
            and rolls it out to <strong>{group}</strong> when it succeeds.
          </span>
        }
        pending={trigger.isPending}
        onConfirm={() => {
          setConfirmRedeploy(false);
          onRedeploy(redeployBody);
        }}
        onCancel={() => setConfirmRedeploy(false)}
      />
    </div>
  );
}

// BuildsList renders the "live now" line, then the env's builds
// (branch-filtered) interleaved with config revisions by time.
function BuildsList({
  project,
  service,
  builds,
  env,
  expanded,
  setExpanded,
  canDeploy,
}: {
  project: string;
  service: string;
  builds: DeployBuild[];
  env?: KusoEnvironment;
  expanded: string | null;
  setExpanded: (id: string | null) => void;
  canDeploy: boolean;
}) {
  const group = envGroupName(env);
  const envCRName = env?.metadata?.name;
  const serviceRevs = useRevisions(project, "service", service);
  const envRevs = useRevisions(
    project,
    "environment",
    envCRName ? envRevisionName(project, envCRName) : undefined,
  );

  const envImage = (env?.spec as { image?: { tag?: string } } | undefined)?.image;
  // Filter to builds matching the active env's branch. Without this
  // filter a PR-branch build would appear under production.
  const envBranch = env?.spec?.branch;
  const visible = envBranch ? builds.filter((b) => (b.branch ?? "") === envBranch) : builds;
  // With liveEnvs the live build is looked up across all branches (a
  // forced rollback can leave the env on another branch's image). The
  // old-server fallback guesses from this env's own branch only.
  const liveId = liveBuildId(hasLiveInfo(builds) ? builds : visible, group, envImage?.tag);
  const live = builds.find((b) => b.id === liveId);

  const liveLine = live ? (
    <LiveNowLine
      build={live}
      rolledBack={isRolledBack(builds, live)}
      branch={envBranch || live.branch || "the branch"}
    />
  ) : null;

  if (visible.length === 0) {
    const total = builds.length;
    if (envBranch && total > 0) {
      const otherBranches = Array.from(new Set(builds.map((b) => b.branch ?? "—"))).filter(
        (b) => b !== envBranch
      );
      return (
        <div className="space-y-2">
          {liveLine}
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
            No builds on branch{" "}
            <span className="font-mono text-[var(--text-secondary)]">{envBranch}</span> yet — service has{" "}
            {total} build{total === 1 ? "" : "s"} on{" "}
            {otherBranches.slice(0, 3).map((b, i) => (
              <span key={b}>
                {i > 0 ? ", " : ""}
                <span className="font-mono text-[var(--text-secondary)]">{b}</span>
              </span>
            ))}
            {otherBranches.length > 3 ? `, +${otherBranches.length - 3} more` : ""}.
          </p>
        </div>
      );
    }
    return (
      <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
        No builds for this environment yet. Click Redeploy above or push to the connected branch.
      </p>
    );
  }

  const timeline = interleaveTimeline(visible, [
    ...(serviceRevs.data ?? []),
    ...(envRevs.data ?? []),
  ]);
  return (
    <div className="space-y-2">
      {liveLine}
      <ul className="space-y-2">
        {timeline.map((item) => {
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
              status={s}
              duration={buildDuration(b, s)}
              isOpen={expanded === b.id}
              canDeploy={canDeploy}
              onToggle={() => setExpanded(expanded === b.id ? null : b.id)}
            />
          );
        })}
      </ul>
    </div>
  );
}
