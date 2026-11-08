"use client";

import type { ReactNode } from "react";
import { AlertTriangle, FileQuestion, Lock, RotateCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { classifyError, errorMessage } from "@/lib/error-kind";
import { cn } from "@/lib/utils";
import { ActionButton, type StateAction } from "./EmptyState";

// ErrorState: a failed load says what failed and offers only what can
// help. Retry appears for server/network failures (when onRetry is
// given); a 404 becomes "not found" with a way back; a 403 explains
// access. Use `inline` inside panels, the default block for whole pages.
//
//   <ErrorState what="project analiz" error={q.error} onRetry={q.refetch}
//     back={{ label: "All projects", href: "/projects" }} />
export function ErrorState({
  what,
  error,
  onRetry,
  back,
  title,
  inline = false,
  className,
  children,
}: {
  /** What was being loaded: "services", "project analiz". */
  what: string;
  error: unknown;
  onRetry?: () => void;
  back?: StateAction;
  title?: string;
  inline?: boolean;
  className?: string;
  children?: ReactNode;
}) {
  const kind = classifyError(error);
  const detail = errorMessage(error);
  const canRetry = !!onRetry && (kind === "server" || kind === "network" || kind === "unknown");

  const heading =
    title ??
    (kind === "not-found"
      ? `Couldn't find ${what}`
      : kind === "forbidden"
        ? `You don't have access to ${what}`
        : kind === "network"
          ? `Couldn't reach the kuso server`
          : `Couldn't load ${what}`);
  const hint =
    kind === "not-found"
      ? "It may have been deleted or renamed, or the link is wrong."
      : kind === "forbidden"
        ? "Ask an admin to grant your role access to it."
        : kind === "network"
          ? "Check your connection; the request never reached the server."
          : null;
  const Icon = kind === "not-found" ? FileQuestion : kind === "forbidden" ? Lock : AlertTriangle;
  const tone = kind === "not-found" || kind === "forbidden" ? "neutral" : "danger";

  if (inline) {
    return (
      <div
        role="alert"
        className={cn(
          "flex items-start gap-3 rounded-lg border p-4 text-sm",
          tone === "danger"
            ? "border-[var(--danger)]/30 bg-[var(--danger-subtle)]"
            : "border-[var(--border-subtle)] bg-[var(--bg-secondary)]",
          className,
        )}
      >
        <Icon
          aria-hidden
          className={cn("mt-0.5 size-4 shrink-0", tone === "danger" ? "text-[var(--danger)]" : "text-[var(--text-tertiary)]")}
        />
        <div className="min-w-0 flex-1">
          <p className="font-medium text-[var(--text-primary)]">{heading}</p>
          <p className="mt-0.5 break-words text-[13px] text-[var(--text-secondary)]">{hint ?? detail}</p>
          {children}
        </div>
        {canRetry && (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            <RotateCw aria-hidden />
            Retry
          </Button>
        )}
        {!canRetry && back && <ActionButton action={back} variant="secondary" />}
      </div>
    );
  }

  return (
    <div role="alert" className={cn("mx-auto flex max-w-md flex-col items-center px-6 py-16 text-center", className)}>
      <div
        aria-hidden
        className={cn(
          "mb-4 flex size-11 items-center justify-center rounded-lg border shadow-[var(--shadow-sm)]",
          tone === "danger"
            ? "border-[var(--danger)]/30 bg-[var(--danger-subtle)] text-[var(--danger)]"
            : "border-[var(--border-subtle)] bg-[var(--bg-elevated)] text-[var(--text-secondary)]",
        )}
      >
        <Icon className="size-5" />
      </div>
      <h2 className="text-base font-semibold tracking-tight text-[var(--text-primary)]">{heading}</h2>
      {hint && <p className="mt-1.5 text-sm text-[var(--text-secondary)]">{hint}</p>}
      {(kind === "server" || kind === "client" || kind === "unknown") && (
        <p className="mt-2 max-w-full break-words rounded-md bg-[var(--bg-secondary)] px-2.5 py-1.5 font-mono text-xs text-[var(--text-secondary)]">
          {detail}
        </p>
      )}
      {children}
      {(canRetry || back) && (
        <div className="mt-5 flex flex-wrap items-center justify-center gap-2">
          {canRetry && (
            <Button variant="primary" size="sm" onClick={onRetry}>
              <RotateCw aria-hidden />
              Try again
            </Button>
          )}
          {back && <ActionButton action={back} variant={canRetry ? "secondary" : "primary"} />}
        </div>
      )}
    </div>
  );
}
