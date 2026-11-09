"use client";

import { useEffect, useState } from "react";
import { GitCompare, GitCommitHorizontal } from "lucide-react";
import type { BuildSummary } from "@/features/services/api";
import { buildRefLabel, githubCommitUrl, githubCompareUrl, type DeployBuild } from "@/features/builds";
import { CopyButton } from "@/components/ui/copy-button";
import { cn } from "@/lib/utils";

// formatDuration turns a millisecond span into "12s", "1m 04s", "1h 02m".
export function formatDuration(ms: number): string {
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

// buildDurationLabel is time-on-task for a finished build, "" otherwise.
export function buildDurationLabel(b: BuildSummary): string {
  const start = b.startedAt ? Date.parse(b.startedAt) : NaN;
  const end = b.finishedAt ? Date.parse(b.finishedAt) : NaN;
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
  return formatDuration(end - start);
}

// LiveDuration owns the 1s tick for a running build so only this span
// re-renders each second.
export function LiveDuration({ since }: { since: string }) {
  const startMs = Date.parse(since);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  if (!Number.isFinite(startMs)) return null;
  return <span className="tabular-nums text-[var(--building)]">{formatDuration(now - startMs)}</span>;
}

// triggerLabel: "by alice", "by bob via push", "via API", …
export function triggerLabel(b: BuildSummary): string {
  const src = b.triggeredBy ?? "";
  const user = b.triggeredByUser ?? "";
  if (src === "user") return user ? `by ${user}` : "by you";
  if (src === "webhook") return user ? `by ${user} via push` : "via push";
  if (src === "api") return "via API";
  if (src === "system") return "via system";
  return "";
}

// actorLabel is who started a build ("by alice"); how is the TriggerChip.
export function actorLabel(b: BuildSummary): string {
  return b.triggeredByUser ? `by ${b.triggeredByUser}` : "";
}


// CommitRef is the short SHA, linked to GitHub when the repo is there, with
// a copy button for the full SHA. Synthetic refs (manual builds with no
// commit) render as plain text.
export function CommitRef({ build, repoUrl, className }: { build: DeployBuild; repoUrl?: string; className?: string }) {
  const short = buildRefLabel(build, 7) || build.id.slice(0, 8);
  const full = build.commitSha || build.ref || build.id;
  const href = githubCommitUrl(repoUrl, build.commitSha);
  return (
    <span className={cn("inline-flex items-center gap-0.5", className)}>
      <GitCommitHorizontal aria-hidden className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
      {href ? (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          title={`View ${full} on GitHub`}
          className="font-mono text-[var(--text-primary)] underline-offset-2 hover:underline"
        >
          {short}
        </a>
      ) : (
        <span className="font-mono text-[var(--text-primary)]" title={full}>
          {short}
        </span>
      )}
      <CopyButton value={full} label="Copy commit SHA" size="xs" />
    </span>
  );
}

export function CompareLink({
  repoUrl,
  from,
  to,
  label = "Compare",
}: {
  repoUrl?: string;
  from?: DeployBuild;
  to: DeployBuild;
  label?: string;
}) {
  const href = githubCompareUrl(repoUrl, from?.commitSha, to.commitSha);
  if (!href || !from) return null;
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      title={`Changes since ${buildRefLabel(from, 7)}`}
      className="inline-flex items-center gap-1 text-[var(--text-secondary)] underline-offset-2 hover:text-[var(--text-primary)] hover:underline"
    >
      <GitCompare aria-hidden className="h-3.5 w-3.5" />
      {label}
    </a>
  );
}

export function Dot() {
  return (
    <span aria-hidden className="text-[var(--text-tertiary)]">
      ·
    </span>
  );
}
