"use client";

import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { AlertTriangle, Check, CheckCircle2, ExternalLink, Loader2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { buildRefLabel, buildStall, type DeployBuild, type DeployStage } from "@/features/builds";
import { cn } from "@/lib/utils";
import { BuildLogViewer } from "./BuildLogViewer";
import { formatDuration, LiveDuration } from "./deployParts";

export type FirstDeployStep = "clone" | "build" | "push" | "start" | "live";

const STEPS: { step: FirstDeployStep; label: string; doing: string }[] = [
  { step: "clone", label: "Clone", doing: "Cloning the repository" },
  { step: "build", label: "Build", doing: "Building the image" },
  { step: "push", label: "Push", doing: "Pushing the image to the registry" },
  { step: "start", label: "Start", doing: "Starting the first instance" },
  { step: "live", label: "Live", doing: "Live" },
];

// FirstDeployProgress is what a service that has never been live shows
// while its first build runs and rolls out: one stepper from clone to
// live, the step in words, and the streaming build log underneath.
export function FirstDeployProgress({
  project,
  service,
  build,
  status,
  starting,
  repoUrl,
}: {
  project: string;
  service: string;
  build: DeployBuild;
  // "start" once the build succeeded and pods are coming up.
  status: "queued" | "pending" | "running" | "start";
  // Pod readiness while starting ("0/1 instances ready").
  starting?: string;
  repoUrl?: string;
}) {
  const [stage, setStage] = useState<DeployStage>("clone");
  const onStage = useCallback((s: DeployStage) => setStage(s), []);
  const current: FirstDeployStep = status === "start" ? "start" : status === "running" ? stage : "clone";
  const idx = STEPS.findIndex((s) => s.step === current);
  const sha = buildRefLabel(build, 7);
  const repo = repoUrl?.replace(/^https?:\/\/(www\.)?(github\.com\/)?/, "");
  const stall = status === "start" ? undefined : buildStall(build, status);

  const headline = stall
    ? stall.summary
    : status === "queued"
      ? build.queuePosition
        ? `Queued behind ${build.queuePosition - 1 > 0 ? `${build.queuePosition - 1} other build${build.queuePosition - 1 === 1 ? "" : "s"}` : "another build"}`
        : "Waiting for a build slot"
      : status === "pending"
        ? "Starting the build"
        : status === "start"
          ? (starting ?? STEPS[3].doing)
          : STEPS[idx].doing;

  return (
    <section
      aria-label="First deploy"
      className="overflow-hidden rounded-lg border border-[var(--building)]/35 bg-[var(--bg-secondary)]"
    >
      <div className="p-4">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--text-secondary)]">
          <span className="inline-flex items-center gap-1.5 font-medium text-[var(--building)]">
            <Loader2 aria-hidden className="h-3 w-3 motion-safe:animate-spin" />
            First deploy
          </span>
          {build.startedAt && (
            <>
              <span aria-hidden>·</span>
              <LiveDuration since={build.startedAt} />
            </>
          )}
          {(sha || build.branch) && (
            <>
              <span aria-hidden>·</span>
              <span className="min-w-0 truncate">
                {repo ? `${repo} ` : ""}
                {build.branch && <span>{build.branch}</span>}
                {sha && <span className="ml-1 font-mono">{sha}</span>}
              </span>
            </>
          )}
        </div>
        <p
          className={cn("mt-2 text-base font-semibold", stall ? "text-[var(--warning)]" : "text-[var(--text-primary)]")}
          role="status"
          aria-live="polite"
        >
          {headline}
        </p>
        {stall?.detail && <p className="mt-0.5 break-words font-mono text-xs text-[var(--text-secondary)]">{stall.detail}</p>}
        {build.commitMessage && (
          <p className="mt-0.5 line-clamp-1 break-words text-sm text-[var(--text-secondary)]">{build.commitMessage}</p>
        )}
        <ol className="mt-4 grid grid-cols-5 gap-1.5 sm:gap-2" aria-label="Deploy progress">
          {STEPS.map((s, i) => {
            const done = i < idx;
            const active = i === idx;
            return (
              <li key={s.step} className="min-w-0" aria-current={active ? "step" : undefined}>
                <div
                  className={cn(
                    "h-1 rounded-full",
                    done
                      ? "bg-[var(--success)]"
                      : active
                        ? stall
                          ? "bg-[var(--warning)]"
                          : "bg-[var(--building)] motion-safe:animate-pulse"
                        : "bg-[var(--border-default)]",
                  )}
                />
                <div
                  className={cn(
                    "mt-1.5 flex items-center gap-1 text-xs",
                    done || active ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]",
                  )}
                >
                  {done ? (
                    <Check aria-hidden className="h-3 w-3 shrink-0 text-[var(--success)]" />
                  ) : active && stall ? (
                    <AlertTriangle aria-hidden className="h-3 w-3 shrink-0 text-[var(--warning)]" />
                  ) : active ? (
                    <Loader2 aria-hidden className="h-3 w-3 shrink-0 text-[var(--building)] motion-safe:animate-spin" />
                  ) : null}
                  <span className="truncate">{s.label}</span>
                  <span className="sr-only">{done ? " (done)" : active ? " (in progress)" : ""}</span>
                </div>
              </li>
            );
          })}
        </ol>
      </div>
      {status !== "start" && (
        <div className="border-t border-[var(--border-subtle)]">
          <BuildLogViewer project={project} service={service} buildId={build.id} live failed={false} onStage={onStage} />
        </div>
      )}
    </section>
  );
}

const NEXT_STEPS: { label: string; tab: string; anchor?: string }[] = [
  { label: "Add variables", tab: "variables" },
  { label: "Watch logs", tab: "logs" },
  { label: "Add a domain", tab: "settings", anchor: "domains" },
  { label: "Set scaling", tab: "settings", anchor: "scale" },
];

const SEEN_KEY = (project: string, service: string) => `kuso:first-live-seen:${project}/${service}`;

export function firstLiveSeen(project: string, service: string): boolean {
  try {
    return window.localStorage.getItem(SEEN_KEY(project, service)) === "1";
  } catch {
    return false;
  }
}

export function markFirstLiveSeen(project: string, service: string) {
  try {
    window.localStorage.setItem(SEEN_KEY(project, service), "1");
  } catch {
    // Private mode: it just shows again next time.
  }
}

// True under a workspace that shows the first-live moment in its header,
// so the Deployments tab doesn't repeat it.
export const FirstLiveHostContext = createContext(false);

// FirstLiveCard closes the first deploy: the URL it's live at, front and
// centre, with open and copy. Dismissal is remembered per service.
export function FirstLiveCard({
  project,
  service,
  url,
  build,
  autoDeploy,
  onNavigate,
}: {
  project: string;
  service: string;
  url: string;
  build: DeployBuild;
  autoDeploy?: boolean;
  onNavigate?: (tab: string, anchor?: string) => void;
}) {
  const [hidden, setHidden] = useState(true);
  const hosted = useContext(FirstLiveHostContext);
  useEffect(() => {
    setHidden(firstLiveSeen(project, service));
  }, [project, service]);
  if (hidden || hosted) return null;
  const dismiss = () => {
    setHidden(true);
    markFirstLiveSeen(project, service);
  };
  const start = build.startedAt ? Date.parse(build.startedAt) : NaN;
  const end = build.finishedAt ? Date.parse(build.finishedAt) : NaN;
  const took = Number.isFinite(start) && Number.isFinite(end) ? formatDuration(end - start) : "";
  const host = url.replace(/^https?:\/\//, "");

  return (
    <section
      aria-label="First deploy finished"
      className="relative rounded-lg border border-[var(--success)]/40 bg-[var(--success-subtle)] p-4"
    >
      <button
        type="button"
        onClick={dismiss}
        aria-label="Dismiss"
        className="absolute right-2 top-2 inline-flex h-7 w-7 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
      >
        <X className="h-4 w-4" />
      </button>
      <div className="flex items-center gap-2 pr-8">
        <CheckCircle2 aria-hidden className="h-4 w-4 shrink-0 text-[var(--success)]" />
        <p className="text-sm font-semibold text-[var(--text-primary)]">
          {service} is live{took ? <span className="font-normal text-[var(--text-secondary)]"> · first deploy took {took}</span> : null}
        </p>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="min-w-0 max-w-full truncate font-mono text-base font-medium text-[var(--text-primary)] underline-offset-4 hover:underline"
        >
          {host}
        </a>
        <CopyButton value={url} label="Copy URL" />
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" render={<a href={url} target="_blank" rel="noreferrer" />} nativeButton={false}>
          <ExternalLink className="h-3.5 w-3.5" />
          Open
        </Button>
        {autoDeploy && (
          <p className="text-xs text-[var(--text-secondary)]">
            Every push to {build.branch ? <span className="font-mono">{build.branch}</span> : "the branch"} deploys from here
            on.
          </p>
        )}
      </div>
      {onNavigate && (
        <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-[var(--success)]/20 pt-3 text-xs text-[var(--text-secondary)]">
          <span>Next:</span>
          {NEXT_STEPS.map((n) => (
            <button
              key={n.label}
              type="button"
              onClick={() => onNavigate(n.tab, n.anchor)}
              className="font-medium text-[var(--text-primary)] underline-offset-2 hover:underline"
            >
              {n.label}
            </button>
          ))}
        </div>
      )}
    </section>
  );
}
