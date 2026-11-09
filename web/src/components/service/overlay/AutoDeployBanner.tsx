"use client";

import { GitBranch, Info, PauseCircle } from "lucide-react";
import type { KusoServiceSpec } from "@/types/projects";
import { githubRepoSlug } from "@/features/builds";
import { Button } from "@/components/ui/button";

export type PushDeploy =
  | { on: true; repo: string; branch: string; watchPaths: string[] }
  | { on: false; paused: true; repo: string; branch: string }
  | { on: false; paused?: false; reason: string; hint: string; action?: { label: string; tab: string; anchor?: string } };

// pushDeployState answers "will a git push deploy this env?" from what the
// server's push dispatcher checks: a GitHub repo on the service (or the
// project default), the instance's GitHub App, and a buildable runtime.
// Besides those, spec.autoDeployPaused is the per-service pause switch.
export function pushDeployState({
  spec,
  projectRepoUrl,
  envBranch,
  githubConfigured,
}: {
  spec: KusoServiceSpec | undefined;
  projectRepoUrl?: string;
  envBranch?: string;
  githubConfigured: boolean | undefined;
}): PushDeploy | null {
  if (!spec) return null;
  if (spec.runtime === "image") {
    return {
      on: false,
      reason: "This service runs a pre-built image, so pushes don't deploy it.",
      hint: "Saving a new image tag rolls out that version.",
      action: { label: "Change the image tag", tab: "settings", anchor: "build" },
    };
  }
  if (spec.runtime === "worker" && spec.fromService) {
    return {
      on: false,
      reason: `This worker reuses ${spec.fromService}'s image.`,
      hint: `It redeploys whenever ${spec.fromService} does.`,
    };
  }
  const repoUrl = spec.repo?.url || projectRepoUrl;
  if (!repoUrl) {
    return {
      on: false,
      reason: "No repository connected, so pushes can't deploy.",
      hint: "",
      action: { label: "Connect a repository", tab: "settings", anchor: "source" },
    };
  }
  const slug = githubRepoSlug(repoUrl);
  if (!slug) {
    return {
      on: false,
      reason: "The repository isn't on GitHub, so pushes don't trigger deploys.",
      hint: "Redeploy by hand, or call the deploy hook (Settings → Deploy) from your CI.",
    };
  }
  const branch = envBranch || spec.repo?.defaultBranch || "main";
  // An explicit pause is the user's own switch: show it (with Resume) even
  // when something else would also stop pushes.
  if (spec.autoDeployPaused) return { on: false, paused: true, repo: slug, branch };
  if (githubConfigured === false) {
    return {
      on: false,
      reason: "The GitHub App isn't set up on this instance, so pushes don't reach kuso.",
      hint: "An admin can connect it under Settings → GitHub.",
    };
  }
  return { on: true, repo: slug, branch, watchPaths: spec.watchPaths ?? [] };
}

export function AutoDeployBanner({
  state,
  onNavigate,
  onResume,
  resuming,
}: {
  state: PushDeploy;
  onNavigate?: (tab: string, anchor?: string) => void;
  // Omitted when the viewer can't edit the service.
  onResume?: () => void;
  resuming?: boolean;
}) {
  if (state.on) {
    return (
      <p className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-[var(--text-secondary)]">
        <GitBranch aria-hidden className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
        Pushes to <span className="font-mono text-[var(--text-primary)]">{state.branch}</span> on{" "}
        <span className="font-mono text-[var(--text-primary)]">{state.repo}</span> deploy automatically
        {state.watchPaths.length > 0 && (
          <span title={state.watchPaths.join(", ")}>
            {" "}
            when they touch <span className="font-mono text-[var(--text-primary)]">{state.watchPaths[0]}</span>
            {state.watchPaths.length > 1 ? ` (+${state.watchPaths.length - 1})` : ""}
          </span>
        )}
        .
      </p>
    );
  }
  if (state.paused) {
    return (
      <div
        role="note"
        className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-[var(--warning)]/40 bg-[var(--warning-subtle)] px-3 py-2.5 text-sm"
      >
        <PauseCircle aria-hidden className="h-4 w-4 shrink-0 text-[var(--warning)]" />
        <div className="min-w-0 flex-1">
          <p className="font-medium text-[var(--text-primary)]">Auto-deploy paused</p>
          <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
            Pushes to <span className="font-mono">{state.branch}</span> on <span className="font-mono">{state.repo}</span>{" "}
            don&apos;t build this service. Manual deploys still work.
          </p>
        </div>
        {onResume && (
          <Button size="sm" variant="secondary" onClick={onResume} disabled={resuming}>
            {resuming ? "Resuming…" : "Resume"}
          </Button>
        )}
      </div>
    );
  }
  const action = state.action;
  return (
    <div
      role="note"
      className="flex items-start gap-2.5 rounded-lg border border-[var(--info)]/35 bg-[var(--info-subtle)] px-3 py-2.5 text-sm"
    >
      <Info aria-hidden className="mt-0.5 h-4 w-4 shrink-0 text-[var(--info)]" />
      <div className="min-w-0">
        <p className="font-medium text-[var(--text-primary)]">Auto-deploy is off</p>
        <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">
          {state.reason} {state.hint}
          {action && onNavigate && (
            <>
              {" "}
              <button
                type="button"
                onClick={() => onNavigate(action.tab, action.anchor)}
                className="font-medium text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--link)]"
              >
                {action.label}
              </button>
            </>
          )}
        </p>
      </div>
    </div>
  );
}
