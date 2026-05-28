"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useBuilds, useTriggerBuild } from "@/features/services";
import { useCan, Perms } from "@/features/auth";
import type { BuildSummary } from "@/features/services/api";
import type { KusoEnvironment } from "@/types/projects";
import { RotateCcw, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import { BuildRow, type BuildRowStatus } from "./BuildRow";

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

// classify maps the raw build status string to the row's visual
// status. ACTIVE is reserved for the build whose imageTag matches the
// env's current image — older successes become SUPERSEDED so the
// "currently live" build is unambiguous.
function classify(b: BuildSummary, activeImageTag?: string): BuildRowStatus {
  const s = (b.status ?? "").toLowerCase();
  if (s === "succeeded") {
    if (!activeImageTag) return "active";
    return b.imageTag && b.imageTag === activeImageTag ? "active" : "superseded";
  }
  if (s === "failed") return "failed";
  if (s === "running") return "running";
  if (s === "pending") return "pending";
  if (s === "queued") return "queued";
  if (s === "cancelled") return "cancelled";
  return "unknown";
}

export function ServiceDeploymentsPanel({ project, service, env }: Props) {
  const builds = useBuilds(project, service);
  const trigger = useTriggerBuild(project, service);
  const [expanded, setExpanded] = useState<string | null>(null);
  const canDeploy = useCan(Perms.ServicesWrite);
  // Re-render every second while at least one build is running so
  // the in-flight duration display ticks visibly.
  const anyRunning = (builds.data ?? []).some(
    (b) => (b.status ?? "").toLowerCase() === "running",
  );
  useNowTick(anyRunning);

  const onRedeploy = async (body: { branch?: string; ref?: string } = {}) => {
    try {
      await trigger.mutateAsync(body);
      toast.success("Build triggered");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to trigger build");
    }
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
          {env?.spec.kind && (
            <span className="font-mono text-[var(--text-tertiary)]">{env.spec.kind}</span>
          )}
        </div>
        {canDeploy ? (
          <Button size="sm" onClick={() => onRedeploy({})} disabled={trigger.isPending}>
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
    </div>
  );
}

// BuildsList does the env-branch filter, computes the current
// active image tag, and renders a BuildRow per build. Extracted out
// of the panel body so the panel's data-fetching + redeploy bar are
// readable without scrolling past 100 lines of rendering.
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
  builds: BuildSummary[];
  env?: KusoEnvironment;
  expanded: string | null;
  setExpanded: (id: string | null) => void;
  canDeploy: boolean;
}) {
  // Filter to builds matching the active env's branch. Without this
  // filter the deployments tab would list every build for the
  // service across every env, so a PR-branch build would appear
  // under production.
  const envBranch = env?.spec?.branch;
  const visible = envBranch ? builds.filter((b) => (b.branch ?? "") === envBranch) : builds;
  if (visible.length === 0) {
    const total = builds.length;
    if (envBranch && total > 0) {
      const otherBranches = Array.from(new Set(builds.map((b) => b.branch ?? "—"))).filter(
        (b) => b !== envBranch
      );
      return (
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
      );
    }
    return (
      <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
        No builds for this environment yet. Trigger one with the button above or push to the connected
        branch.
      </p>
    );
  }
  const envImage = (env?.spec as { image?: { tag?: string } } | undefined)?.image;
  const activeTag = envImage?.tag;
  return (
    <ul className="space-y-2">
      {visible.map((b) => {
        const s = classify(b, activeTag);
        return (
          <BuildRow
            key={b.id}
            project={project}
            service={service}
            env={
              // Prefer the env-group label (production / staging /
              // preview-pr-N) so the rollback API addresses the
              // right CR. Falls back to "production" on legacy
              // env CRs without the label.
              env?.metadata?.labels?.["kuso.sislelabs.com/env"] ??
              env?.spec?.kind ??
              "production"
            }
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
  );
}
