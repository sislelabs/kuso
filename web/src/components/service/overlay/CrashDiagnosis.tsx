"use client";

import { ShieldAlert } from "lucide-react";
import { exitMeaning, useCrashReport, type CrashReport } from "@/features/builds";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// CrashDiagnosis puts the crash's evidence where the failure is reported:
// how the container last exited, the last lines the dying run printed and,
// when kuso recognises the failure, what to change. Renders nothing until
// the server has a crash to report (older servers never do).
export function CrashDiagnosis({
  project,
  service,
  envName,
  onNavigate,
  className,
  framed = false,
}: {
  project: string;
  service: string;
  // env CR name
  envName?: string;
  onNavigate?: (tab: string, anchor?: string) => void;
  className?: string;
  // Standalone card (nothing else on screen states the crash in detail).
  framed?: boolean;
}) {
  const q = useCrashReport(project, service, envName, true);
  const crash = q.data;
  if (!crash) return null;
  if (!framed) return <CrashDetails crash={crash} onNavigate={onNavigate} className={className} />;
  return (
    <section
      aria-labelledby="crash-diagnosis-h"
      className={cn("rounded-lg border border-[var(--error)]/40 bg-[var(--bg-secondary)] p-4", className)}
    >
      <h4 id="crash-diagnosis-h" className="mb-2 text-sm font-semibold text-[var(--text-primary)]">
        Why it keeps crashing
      </h4>
      <CrashDetails crash={crash} onNavigate={onNavigate} />
    </section>
  );
}

export function CrashDetails({
  crash,
  onNavigate,
  className,
}: {
  crash: CrashReport;
  onNavigate?: (tab: string, anchor?: string) => void;
  className?: string;
}) {
  const cls = crash.classification;
  const capability = cls?.kind === "missing_capability";
  const meaning = exitMeaning(crash.exitCode, crash.reason);
  // The server may have clipped the hint with an ellipsis.
  const hint = cls?.lineHint?.trim().replace(/…$/, "");
  return (
    <div className={cn("space-y-2.5", className)} aria-label="Last crash">
      <p className="text-xs text-[var(--text-secondary)]">
        <span className="font-medium text-[var(--text-primary)]">Last exit</span>
        {": "}
        {crash.reason && crash.reason !== "Error" ? `${crash.reason}, ` : ""}
        exit code <span className="font-mono text-[var(--text-primary)]">{crash.exitCode}</span>
        {meaning ? ` (${meaning})` : ""}
        {crash.finishedAt && (
          <>
            {" · "}
            <time dateTime={crash.finishedAt} title={new Date(crash.finishedAt).toLocaleString()}>
              {relativeTime(crash.finishedAt)}
            </time>
          </>
        )}
        {crash.restarts > 0 && ` · restarted ${crash.restarts}×`}
      </p>

      {capability && cls ? (
        <div className="rounded-md border border-[var(--border-default)] bg-[var(--bg-primary)] p-3">
          <p className="flex items-start gap-2 text-sm font-medium text-[var(--text-primary)]">
            <ShieldAlert aria-hidden className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" />
            {cls.summary}
          </p>
          {cls.remediation?.detail && (
            <p className="mt-1 text-xs leading-relaxed text-[var(--text-secondary)]">{cls.remediation.detail}</p>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {onNavigate && (
              <Button size="sm" variant="secondary" onClick={() => onNavigate("settings", "security")}>
                {cls.remediation?.title ?? "Security settings"}
              </Button>
            )}
            {cls.remediation?.fix && (
              <span className="inline-flex min-w-0 items-center gap-1 rounded-md border border-[var(--border-subtle)] px-2 py-1 font-mono text-xs text-[var(--text-secondary)]">
                <span className="min-w-0 truncate" title={cls.remediation.fix}>
                  {cls.remediation.fix}
                </span>
                <CopyButton value={cls.remediation.fix} label="Copy command" size="xs" />
              </span>
            )}
          </div>
        </div>
      ) : cls ? (
        <p className="text-sm text-[var(--text-primary)]">
          {cls.summary}
          {cls.remediation?.title ? <span className="text-[var(--text-secondary)]"> {cls.remediation.title}.</span> : null}
        </p>
      ) : null}

      {crash.log.length > 0 ? (
        <div className="overflow-hidden rounded-md border border-[var(--border-subtle)]">
          <div className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-2.5 py-1 text-xs text-[var(--text-tertiary)]">
            <span>
              Last {crash.log.length} line{crash.log.length === 1 ? "" : "s"} before it exited
            </span>
            {onNavigate && (
              <button
                type="button"
                onClick={() => onNavigate("logs")}
                className="text-[var(--text-secondary)] underline underline-offset-2 hover:text-[var(--text-primary)]"
              >
                Open logs
              </button>
            )}
          </div>
          <pre className="max-h-48 overflow-auto bg-[var(--console-bg)] p-2 font-mono text-xs leading-relaxed text-[var(--console-fg)]">
            {crash.log.map((l, i) => (
              <span
                key={i}
                className={cn("block whitespace-pre-wrap break-words", hint && l.includes(hint) && "bg-[var(--error)]/20")}
              >
                {l}
              </span>
            ))}
          </pre>
        </div>
      ) : (
        <p className="text-xs text-[var(--text-tertiary)]">The container exited without printing anything.</p>
      )}
    </div>
  );
}
