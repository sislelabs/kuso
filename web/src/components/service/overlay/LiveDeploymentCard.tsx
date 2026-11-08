"use client";

import { useState } from "react";
import { ChevronDown, ExternalLink, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { CopyButton } from "@/components/ui/copy-button";
import { buildRefLabel, useRollbackToBuild, type DeployBuild } from "@/features/builds";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { BuildLogViewer } from "./BuildLogViewer";
import { buildDurationLabel, CommitRef, CompareLink, Dot, triggerLabel } from "./deployParts";

export type LiveHealth = { label: string; tone: "ok" | "warn" | "bad" | "idle" };

const HEALTH_TONE: Record<LiveHealth["tone"], string> = {
  ok: "bg-[var(--success)]",
  warn: "bg-[var(--warning)]",
  bad: "bg-[var(--error)]",
  idle: "bg-[var(--text-tertiary)]",
};

// LiveDeploymentCard is the one answer to "what is serving this env right
// now": commit, who, when it went live, the URL, health, and — when the env
// was rolled back — what it was rolled back from with an Undo.
export function LiveDeploymentCard({
  project,
  service,
  envGroup,
  build,
  previous,
  rolledBackFrom,
  repoUrl,
  url,
  deployedAt,
  health,
  serving = true,
  canDeploy,
  extra,
}: {
  project: string;
  service: string;
  envGroup: string;
  build: DeployBuild;
  previous?: DeployBuild;
  // Set when a newer successful build exists on this branch: the env was
  // pointed back at `build` and this is what "Undo rollback" restores.
  rolledBackFrom?: DeployBuild;
  repoUrl?: string;
  url?: string;
  deployedAt?: string;
  health?: LiveHealth;
  // False while the env is stopped or asleep: this is still the current
  // deployment, but nothing is serving it.
  serving?: boolean;
  canDeploy: boolean;
  extra?: React.ReactNode;
}) {
  const [logOpen, setLogOpen] = useState(false);
  const went = deployedAt || build.finishedAt || build.startedAt;
  const who = triggerLabel(build);
  const dur = buildDurationLabel(build);
  const rolledBack = !!rolledBackFrom;

  return (
    <section
      aria-label={`${serving ? "Live" : "Current deployment"} in ${envGroup}`}
      className={cn(
        "overflow-hidden rounded-lg border bg-[var(--bg-secondary)]",
        rolledBack ? "border-[var(--warning)]/45" : serving ? "border-[var(--success)]/35" : "border-[var(--border-default)]",
      )}
    >
      <div className="space-y-3 p-4">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          {serving ? (
            <span className="inline-flex items-center gap-1.5 font-medium text-[var(--success)]">
              <span className="relative flex h-2 w-2">
                <span className="absolute inline-flex h-full w-full rounded-full bg-[var(--success)] opacity-60 motion-safe:animate-ping" />
                <span className="relative inline-flex h-2 w-2 rounded-full bg-[var(--success)]" />
              </span>
              Live
            </span>
          ) : (
            <span className="inline-flex items-center gap-1.5 font-medium text-[var(--text-primary)]">
              <span className="h-2 w-2 rounded-full bg-[var(--text-tertiary)]" />
              Current deployment
            </span>
          )}
          <span className="text-[var(--text-secondary)]">in {envGroup}</span>
          {health && (
            <span className="inline-flex items-center gap-1.5 rounded-full border border-[var(--border-subtle)] px-2 py-0.5 text-[var(--text-secondary)]">
              <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", HEALTH_TONE[health.tone])} />
              {health.label}
            </span>
          )}
          {extra}
          {rolledBack && (
            <span className="rounded-full border border-[var(--warning)]/45 bg-[var(--warning-subtle)] px-2 py-0.5 font-medium text-[var(--warning)]">
              Rolled back
            </span>
          )}
        </div>

        <div className="min-w-0">
          <p className="break-words text-base font-semibold leading-snug text-[var(--text-primary)]">
            {build.commitMessage || (build.triggeredBy === "user" ? "Manual redeploy" : `Deploy ${buildRefLabel(build, 7) || build.id}`)}
          </p>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-[var(--text-secondary)]">
            <CommitRef build={build} repoUrl={repoUrl} />
            <Dot />
            <span className="max-w-[18ch] truncate" title={build.branch}>
              {build.branch ?? "—"}
            </span>
            {who && (
              <>
                <Dot />
                <span>{who}</span>
              </>
            )}
            {went && (
              <>
                <Dot />
                <time dateTime={went} title={new Date(went).toLocaleString()}>
                  deployed {relativeTime(went)}
                </time>
              </>
            )}
            {dur && (
              <>
                <Dot />
                <span title="Build time">built in {dur}</span>
              </>
            )}
          </div>
        </div>

        {url && (
          <div className="flex min-w-0 items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] py-1 pl-2.5 pr-1">
            <a
              href={url}
              target="_blank"
              rel="noreferrer"
              className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--text-primary)] hover:underline"
            >
              {url.replace(/^https?:\/\//, "")}
            </a>
            <CopyButton value={url} label="Copy URL" />
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

        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs">
          <button
            type="button"
            onClick={() => setLogOpen((o) => !o)}
            aria-expanded={logOpen}
            className="inline-flex items-center gap-1 text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
          >
            <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", logOpen && "rotate-180")} />
            Build log
          </button>
          <CompareLink repoUrl={repoUrl} from={previous} to={build} label={previous ? `Changes since ${buildRefLabel(previous, 7)}` : undefined} />
        </div>
      </div>

      {rolledBackFrom && (
        <RolledBackStrip
          project={project}
          service={service}
          envGroup={envGroup}
          from={rolledBackFrom}
          branch={build.branch}
          canDeploy={canDeploy}
        />
      )}

      {logOpen && (
        <div className="border-t border-[var(--border-subtle)]">
          <BuildLogViewer project={project} service={service} buildId={build.id} live={false} failed={false} />
        </div>
      )}
    </section>
  );
}

function RolledBackStrip({
  project,
  service,
  envGroup,
  from,
  branch,
  canDeploy,
}: {
  project: string;
  service: string;
  envGroup: string;
  from: DeployBuild;
  branch?: string;
  canDeploy: boolean;
}) {
  const [confirm, setConfirm] = useState(false);
  const m = useRollbackToBuild(project, service, envGroup);
  const sha = buildRefLabel(from, 7) || from.id;
  return (
    <div className="flex flex-wrap items-center gap-3 border-t border-[var(--warning)]/30 bg-[var(--warning-subtle)] px-4 py-2.5 text-xs text-[var(--text-secondary)]">
      <p className="min-w-0 flex-1">
        Rolled back from <span className="font-mono text-[var(--text-primary)]">{sha}</span>. The next push to{" "}
        <span className="font-mono text-[var(--text-primary)]">{branch || "the branch"}</span> deploys over this.
      </p>
      {canDeploy && (
        <button
          type="button"
          onClick={() => setConfirm(true)}
          disabled={m.isPending}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--warning)]/50 bg-[var(--bg-primary)] px-2.5 font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] disabled:opacity-50"
        >
          <Undo2 className="h-3.5 w-3.5 -scale-x-100" />
          Undo rollback
        </button>
      )}
      <ConfirmDialog
        open={confirm}
        title={`Undo rollback on ${envGroup}?`}
        destructive={false}
        confirmLabel={`Deploy ${sha}`}
        body={
          <span>
            {envGroup} goes back to <span className="font-mono">{sha}</span>
            {from.commitMessage ? <> ({from.commitMessage})</> : null}. Its image is reused, nothing rebuilds.
          </span>
        }
        pending={m.isPending}
        onConfirm={() =>
          m.mutate(
            { build: from.id },
            {
              onSuccess: () => {
                toast.success(`${envGroup} is back on ${sha}`);
                setConfirm(false);
              },
              onError: (e) => {
                toast.error(e instanceof Error ? e.message : "Couldn't undo the rollback");
                setConfirm(false);
              },
            },
          )
        }
        onCancel={() => setConfirm(false)}
      />
    </div>
  );
}
