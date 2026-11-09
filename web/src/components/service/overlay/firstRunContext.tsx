"use client";

import { createContext, useContext } from "react";
import { Hourglass } from "lucide-react";
import type { FirstRun } from "./firstRun";

type Ctx = { firstRun: FirstRun; onNavigate?: (tab: string, anchor?: string) => void };

const FirstRunContext = createContext<Ctx>({ firstRun: { kind: "ran" } });

export const FirstRunProvider = FirstRunContext.Provider;

export function useFirstRun(): Ctx {
  return useContext(FirstRunContext);
}

// buildLogAnchor is the Deployments row that holds the last build's log
// (the panel auto-expands the newest failed build).
export function buildLogAnchor(fr: FirstRun): string | undefined {
  return fr.kind === "never" && fr.lastBuild ? `build-row-${fr.lastBuild.id}` : undefined;
}

// neverStartedCause is the one sentence every surface uses for why
// there's nothing to show yet.
export function neverStartedCause(fr: FirstRun): string {
  if (fr.kind !== "never") return "";
  if (fr.building) return "The first build is still running.";
  if (fr.failed) return "The last build failed.";
  if (fr.lastBuild) return "The last build hasn't been deployed.";
  return "Nothing has been built yet.";
}

// NeverStartedState fills Pods, Logs and Metrics for a service that has
// never run in this environment, with one way to the build log.
export function NeverStartedState({ title, compact = false }: { title: string; compact?: boolean }) {
  const { firstRun, onNavigate } = useFirstRun();
  if (firstRun.kind !== "never") return null;
  const anchor = buildLogAnchor(firstRun);
  const action = firstRun.lastBuild ? (firstRun.building ? "Follow the build" : "View build log") : "Open Deployments";
  return (
    <div className={compact ? "px-3 py-3 text-xs" : "flex flex-col items-center gap-2 px-6 py-14 text-center text-sm"}>
      {!compact && <Hourglass aria-hidden className="h-5 w-5 text-[var(--text-tertiary)]" />}
      <p className={compact ? "text-[var(--text-secondary)]" : "font-medium text-[var(--text-primary)]"}>{title}</p>
      <p className="text-[var(--text-secondary)]">
        {neverStartedCause(firstRun)}{" "}
        {onNavigate && (
          <button
            type="button"
            onClick={() => onNavigate("deployments", anchor)}
            className="font-medium text-[var(--link)] underline-offset-2 hover:underline"
          >
            {action}
          </button>
        )}
      </p>
    </div>
  );
}
