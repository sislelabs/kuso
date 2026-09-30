"use client";

import type { DeployBuild } from "@/features/builds";
import { relativeTime } from "@/lib/format";
import { triggerLabel } from "./BuildRow";

// LiveNowLine: one line naming the build currently serving this env.
export function LiveNowLine({
  build,
  rolledBack,
  branch,
}: {
  build: DeployBuild;
  rolledBack: boolean;
  branch: string;
}) {
  const sha = (build.commitSha ?? "").slice(0, 7);
  const when = relativeTime(build.finishedAt ?? build.startedAt);
  const who = triggerLabel(build);
  return (
    <div className="flex min-w-0 items-center gap-2 rounded-md border border-emerald-500/30 bg-[var(--bg-secondary)] px-3 py-2 text-xs">
      <span className="shrink-0 font-mono text-[9px] font-semibold tracking-widest text-emerald-400">
        LIVE
      </span>
      <span className="shrink-0 font-mono text-[var(--text-primary)]">{sha || build.id.slice(0, 8)}</span>
      {build.commitMessage && (
        <span className="min-w-0 truncate text-[var(--text-secondary)]" title={build.commitMessage}>
          {build.commitMessage}
        </span>
      )}
      <span className="ml-auto shrink-0 font-mono text-[10px] text-[var(--text-tertiary)]">
        {[who, when].filter(Boolean).join(" · ")}
      </span>
      {rolledBack && (
        <span
          className="shrink-0 rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-widest text-amber-400"
          title={`The next push to ${branch} will deploy over this`}
        >
          ROLLED BACK
        </span>
      )}
    </div>
  );
}
