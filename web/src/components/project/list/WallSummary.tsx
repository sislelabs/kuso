"use client";

import Link from "next/link";
import { formatCpu, relativeTime } from "@/lib/format";
import { SERVICE_STATUS, TONE_CLASSES } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { AttentionItem } from "./fleetModel";
import { formatBytes } from "./projectCardModel";

const inlineLink =
  "rounded-sm text-[var(--text-primary)] underline decoration-[var(--border-strong)] underline-offset-[3px] outline-none hover:decoration-[var(--text-primary)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]";

const MAX_PROBLEMS = 4;

// The fleet in plain sentences: how much runs, where the errors come
// from, what it costs, and which project shipped last.
export function WallSummary({
  running,
  stopped,
  errors,
  topErrorProject,
  memBytes,
  cpuMillicores,
  lastDeploy,
}: {
  running: number;
  stopped: number;
  errors: number | undefined;
  topErrorProject: { name: string; count: number } | undefined;
  memBytes: number;
  cpuMillicores: number;
  lastDeploy: { name: string; at: string } | undefined;
}) {
  return (
    <span className="block max-w-3xl text-[15px] leading-relaxed text-[var(--text-secondary)]">
      {running} {running === 1 ? "project is" : "projects are"} running
      {stopped > 0 && <>, {stopped} stopped</>}.{" "}
      {errors !== undefined &&
        (errors === 0 ? (
          <>No runtime errors in the last 24 hours. </>
        ) : (
          <>
            <span className="text-[var(--danger)]">{errors.toLocaleString()} errors</span> in the last 24 hours
            {topErrorProject && (
              <>
                , {topErrorProject.count === errors ? "all" : "most"} from{" "}
                <Link href={`/projects/${topErrorProject.name}`} className={inlineLink}>
                  {topErrorProject.name}
                </Link>
              </>
            )}
            .{" "}
          </>
        ))}
      {memBytes > 0 && (
        <>
          {formatBytes(memBytes)} memory and {formatCpu(cpuMillicores)} in use.{" "}
        </>
      )}
      {lastDeploy && (
        <>
          Last deploy was{" "}
          <Link href={`/projects/${lastDeploy.name}`} className={inlineLink}>
            {lastDeploy.name}
          </Link>
          , {relativeTime(lastDeploy.at)}.
        </>
      )}
    </span>
  );
}

// Only shown when something is broken or rolling out, as redline notes
// on the drawing: each line opens that service.
export function ProblemList({ items }: { items: ReadonlyArray<AttentionItem> }) {
  if (items.length === 0) return null;
  const shown = items.slice(0, MAX_PROBLEMS);
  const broken = items.some((a) => a.tone === "danger" || a.tone === "warning");
  return (
    <div
      className={cn(
        "mb-8 overflow-hidden rounded-lg border",
        broken ? "border-[var(--danger)]/35 bg-[var(--danger-subtle)]" : "border-[var(--building)]/35 bg-[var(--building-subtle)]",
      )}
    >
      <ul className="divide-y divide-[var(--border-subtle)]" aria-label="Services that need attention">
        {shown.map((a) => {
          const meta = SERVICE_STATUS[a.status];
          return (
            <li key={`${a.project}/${a.service}`}>
              <Link
                href={`/projects/${a.project}?service=${encodeURIComponent(a.service)}`}
                className="flex min-w-0 items-center gap-3 bg-[var(--bg-elevated)]/70 px-4 py-2.5 text-[13px] outline-none hover:bg-[var(--bg-elevated)] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--ring)]"
              >
                <span aria-hidden className={cn("size-2 shrink-0 rounded-full", TONE_CLASSES[a.tone].dot, meta.pulse && "motion-safe:animate-pulse")} />
                <span className="shrink-0 font-medium text-[var(--text-primary)]">
                  {a.project}
                  <span className="px-1 text-[var(--text-tertiary)]">/</span>
                  {a.service}
                </span>
                <span className={cn("shrink-0 font-medium", TONE_CLASSES[a.tone].text)}>{meta.label}</span>
                {a.detail && <span className="min-w-0 truncate text-[var(--text-secondary)]">{a.detail}</span>}
                <span aria-hidden className="ml-auto shrink-0 text-xs text-[var(--text-tertiary)]">
                  Open
                </span>
              </Link>
            </li>
          );
        })}
      </ul>
      {items.length > shown.length && (
        <p className="border-t border-[var(--border-subtle)] px-4 py-2 text-xs text-[var(--text-secondary)]">
          {items.length - shown.length} more, marked on the sheets below
        </p>
      )}
    </div>
  );
}
