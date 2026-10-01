"use client";

import { useMemo, useState } from "react";
import { useErrors } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { serviceEnvOptions } from "@/lib/env-group";
import type { ErrorGroup } from "@/features/services";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { ChevronDown, ChevronRight, AlertTriangle } from "lucide-react";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
  // Switches the overlay to the Logs tab. The Logs tab has no time
  // anchor yet, so this lands on its default (latest) view.
  onViewLogs?: () => void;
}

const SINCE_OPTIONS: { id: string; label: string }[] = [
  { id: "1h", label: "1 hour" },
  { id: "24h", label: "24 hours" },
  // Log retention is 7 days, so a longer window would only look empty.
  { id: "7d", label: "7 days" },
];

// ServiceErrorsPanel renders the Sentry-style error feed for a
// service. Source of truth: GET /api/projects/{p}/services/{s}/errors,
// which aggregates ErrorEvent rows by fingerprint server-side. The
// panel just renders + offers a since filter; everything else lives
// in the backend.
//
// Layout per row:
//   [icon] <message>                   <count>
//          first seen X ago · last Y ago · env / pod
//          (expanded) raw line
export function ServiceErrorsPanel({ project, service, onViewLogs }: Props) {
  const [since, setSince] = useState("24h");
  // "" = every environment. Not pre-set from the overlay's env: error
  // events only carry an env for pods labelled kuso.sislelabs.com/env.
  const [env, setEnv] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const errors = useErrors(project, service, since, env);
  const envs = useEnvironments(project);
  const fqn = service ? project + "-" + service : "";
  const envOptions = useMemo(() => serviceEnvOptions(envs.data ?? [], fqn), [envs.data, fqn]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="text-xs text-[var(--text-secondary)]">
          {errors.isError ? (
            <>Error groups in last {since}</>
          ) : (
            <>
              {errors.data?.length ?? 0}{" "}
              {errors.data?.length === 1 ? "error group" : "error groups"} in last {since}
            </>
          )}
        </div>
        <div className="flex items-center gap-1">
          <select
            value={env}
            onChange={(e) => setEnv(e.target.value)}
            aria-label="Environment"
            className="mr-1 h-6 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 font-mono text-[10px]"
          >
            <option value="">all envs</option>
            {envOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          {SINCE_OPTIONS.map((opt) => (
            <button
              key={opt.id}
              type="button"
              onClick={() => setSince(opt.id)}
              className={cn(
                "rounded-md border px-2 py-1 font-mono text-[10px]",
                since === opt.id
                  ? "border-[var(--accent)]/40 bg-[var(--accent)]/10 text-[var(--accent)]"
                  : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]",
              )}
            >
              {opt.label}
            </button>
          ))}
        </div>
      </div>

      {errors.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : errors.isError ? (
        <QueryErrorState what="errors" error={errors.error} onRetry={() => void errors.refetch()} />
      ) : (errors.data?.length ?? 0) === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          No errors detected in the last {since}. The scanner watches pod logs for
          ERROR / Exception / panic / Traceback patterns, so errors logged in another
          shape won&apos;t appear here. Log retention is 7 days.
        </p>
      ) : (
        <ul className="space-y-2">
          {errors.data!.map((g) => (
            <ErrorRow
              key={g.fingerprint}
              group={g}
              isOpen={expanded === g.fingerprint}
              onToggle={() => setExpanded(expanded === g.fingerprint ? null : g.fingerprint)}
              onViewLogs={onViewLogs}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

function ErrorRow({
  group,
  isOpen,
  onToggle,
  onViewLogs,
}: {
  group: ErrorGroup;
  isOpen: boolean;
  onToggle: () => void;
  onViewLogs?: () => void;
}) {
  return (
    <li className="overflow-hidden rounded-md border border-red-500/20 bg-red-500/[0.03]">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-start gap-2 px-3 py-2.5 text-left"
      >
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-400" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm text-[var(--text-primary)]">
            {group.message || "(empty error)"}
          </div>
          <div className="font-mono text-[10px] text-[var(--text-tertiary)]">
            first {relativeTime(group.firstSeen)} · last {relativeTime(group.lastSeen)}
            {group.sampleEnv ? ` · ${group.sampleEnv}` : ""}
            {group.samplePod ? ` · ${group.samplePod}` : ""}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span
            className="rounded-md border border-red-500/30 bg-red-500/10 px-2 py-0.5 font-mono text-xs text-red-300"
            title={`${group.count} occurrences`}
          >
            ×{group.count}
          </span>
          {isOpen ? (
            <ChevronDown className="h-4 w-4 text-[var(--text-tertiary)]" />
          ) : (
            <ChevronRight className="h-4 w-4 text-[var(--text-tertiary)]" />
          )}
        </div>
      </button>
      {isOpen && (
        <div className="border-t border-red-500/10 bg-[var(--bg-primary)] p-3">
          <div className="mb-1 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Sample line
          </div>
          <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-xs text-[var(--text-secondary)]">
            {group.sampleLine}
          </pre>
          <div className="mt-2 flex items-center justify-between gap-2 font-mono text-[10px] text-[var(--text-tertiary)]">
            <span className="truncate">fingerprint: {group.fingerprint}</span>
            {onViewLogs ? (
              <button
                type="button"
                onClick={onViewLogs}
                title={`Last seen ${relativeTime(group.lastSeen)}`}
                className="shrink-0 underline underline-offset-2 hover:text-[var(--text-primary)]"
              >
                View in logs →
              </button>
            ) : null}
          </div>
        </div>
      )}
    </li>
  );
}
