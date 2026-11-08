"use client";

import { useEffect, useMemo, useState } from "react";
import { Check, ExternalLink, Loader2, X, XCircle } from "lucide-react";
import type { KusoEnvironment, KusoService } from "@/types/projects";
import { useLatestBuilds } from "@/features/services";
import { isProductionGroup } from "@/lib/env-group";
import { cn, serviceShortName } from "@/lib/utils";
import { CopyButton } from "@/components/ui/copy-button";
import { firstDeployProgress, pickFirstDeploy, type FirstDeployStep } from "./firstDeploy";

const dismissKey = (project: string) => `kuso.firstdeploy.dismissed.${project}`;

function loadDismissed(project: string): boolean {
  try {
    return localStorage.getItem(dismissKey(project)) === "1";
  } catch {
    return false;
  }
}

// FirstDeployChecklist follows a project's first service from repo to a
// live URL (Repo connected → First build → Healthy → Live at <url>),
// updating with the canvas's own polls. It shows only while nothing in
// the project has ever been live, flips to a success line when the first
// URL comes up, and can be dismissed for good.
export function FirstDeployChecklist({
  project,
  selectedEnv,
  services,
  envs,
  onOpenService,
  className,
}: {
  project: string;
  selectedEnv: string;
  services: KusoService[];
  envs: KusoEnvironment[];
  onOpenService?: (service: string, tab?: string) => void;
  className?: string;
}) {
  const latestBuilds = useLatestBuilds(project, selectedEnv);
  const [dismissed, setDismissed] = useState(true);
  useEffect(() => setDismissed(loadDismissed(project)), [project]);

  const pick = useMemo(() => {
    const snaps = services.map((service) => {
      const mine = envs.filter((e) => e.spec.service === service.metadata.name);
      return {
        service,
        env: mine.find((e) => isProductionGroup(e)) ?? mine[0],
        build: latestBuilds.data?.[serviceShortName(project, service.metadata.name)],
      };
    });
    return pickFirstDeploy(project, snaps);
  }, [project, services, envs, latestBuilds.data]);

  // Pin the service the checklist started with, so it can show its own
  // "Live at …" finish instead of vanishing the moment it goes live.
  const [followed, setFollowed] = useState<string | null>(null);
  useEffect(() => {
    if (pick && !followed) setFollowed(pick.service.metadata.name);
  }, [pick, followed]);

  const snap = useMemo(() => {
    if (pick) return pick;
    if (!followed) return null;
    const service = services.find((s) => s.metadata.name === followed);
    if (!service) return null;
    const mine = envs.filter((e) => e.spec.service === followed);
    return {
      service,
      env: mine.find((e) => isProductionGroup(e)) ?? mine[0],
      build: latestBuilds.data?.[serviceShortName(project, followed)],
    };
  }, [pick, followed, services, envs, latestBuilds.data, project]);

  if (dismissed || !snap || selectedEnv !== "production" || latestBuilds.isPending) return null;
  const progress = firstDeployProgress(project, snap);

  const dismiss = () => {
    setDismissed(true);
    try {
      localStorage.setItem(dismissKey(project), "1");
    } catch {
      // storage disabled: it just comes back on reload
    }
  };

  return (
    <section
      aria-label="First deploy progress"
      className={cn(
        "shrink-0 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-4 py-2.5 sm:px-6",
        className,
      )}
    >
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        <p className="text-xs font-medium text-[var(--text-primary)]">
          {progress.live ? (
            <>
              <span className="text-[var(--success)]">{progress.displayName} is live</span>
            </>
          ) : (
            <>Getting {progress.displayName} live</>
          )}
        </p>
        <ol className="flex flex-wrap items-center gap-x-3 gap-y-1.5 sm:gap-x-1" aria-live="polite">
          {progress.steps.map((step, i) => (
            <li key={step.id} className="flex items-center gap-1">
              {i > 0 && <span aria-hidden className="mx-1 hidden h-px w-4 bg-[var(--border-default)] sm:inline-block" />}
              <StepChip step={step} url={step.id === "live" ? progress.url : undefined} />
            </li>
          ))}
        </ol>
        <div className="ml-auto flex items-center gap-1">
          {progress.failed && (
            <button
              type="button"
              onClick={() => onOpenService?.(progress.service, "deployments")}
              className="h-7 rounded-md px-2 text-xs font-medium text-[var(--error)] hover:bg-[var(--error-subtle)] focus-visible:outline-2 focus-visible:outline-[var(--focus)]"
            >
              See what failed
            </button>
          )}
          {!progress.live && !progress.failed && (
            <button
              type="button"
              onClick={() => onOpenService?.(progress.service, "deployments")}
              className="h-7 rounded-md px-2 text-xs font-medium text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:outline-2 focus-visible:outline-[var(--focus)]"
            >
              Watch the build
            </button>
          )}
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss first deploy progress"
            title="Dismiss"
            className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus-visible:outline-2 focus-visible:outline-[var(--focus)]"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </section>
  );
}

function StepChip({ step, url }: { step: FirstDeployStep; url?: string }) {
  const icon =
    step.state === "done" ? (
      <Check className="h-3 w-3" aria-hidden />
    ) : step.state === "active" ? (
      <Loader2 className="h-3 w-3 motion-safe:animate-spin" aria-hidden />
    ) : step.state === "failed" ? (
      <XCircle className="h-3 w-3" aria-hidden />
    ) : (
      <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-current" />
    );
  const tone =
    step.state === "done"
      ? "text-[var(--success)]"
      : step.state === "active"
        ? "text-[var(--building)]"
        : step.state === "failed"
          ? "text-[var(--error)]"
          : "text-[var(--text-tertiary)]";
  const stateWord = { done: "done", active: "in progress", failed: "failed", todo: "not started" }[step.state];

  if (step.id === "live" && step.state === "done" && url) {
    const host = url.replace(/^https?:\/\//, "");
    return (
      <span className="inline-flex items-center gap-1.5 text-xs">
        <span className={cn("inline-flex h-4 w-4 items-center justify-center", tone)}>{icon}</span>
        <span className="text-[var(--text-secondary)]">Live at</span>
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 font-mono text-[var(--link)] underline-offset-2 hover:underline"
        >
          {host}
          <ExternalLink className="h-3 w-3" aria-hidden />
        </a>
        <CopyButton value={url} label="Copy URL" />
      </span>
    );
  }

  return (
    <span
      className="inline-flex items-center gap-1.5 text-xs"
      title={step.detail}
      aria-label={`${step.label}: ${stateWord}${step.detail ? `. ${step.detail}` : ""}`}
    >
      <span className={cn("inline-flex h-4 w-4 items-center justify-center", tone)}>{icon}</span>
      <span className={step.state === "todo" ? "text-[var(--text-tertiary)]" : "text-[var(--text-primary)]"}>
        {step.label}
      </span>
      {step.detail && step.state !== "todo" && (
        <span
          className={cn(
            "hidden max-w-[16rem] truncate md:inline",
            step.id === "source" || step.id === "live" ? "font-mono text-[var(--text-tertiary)]" : "text-[var(--text-tertiary)]",
          )}
        >
          {step.detail}
        </span>
      )}
    </span>
  );
}
