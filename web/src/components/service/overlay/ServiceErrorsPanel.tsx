"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, ChevronDown, ChevronRight, Copy, ScrollText } from "lucide-react";
import { useErrors } from "@/features/services";
import type { ErrorGroup } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { fetchErrorTrace, getErrorDetail } from "@/features/logs";
import type { ErrorGroupDetail, ErrorOccurrence } from "@/features/logs";
import { serviceEnvOptions } from "@/lib/env-group";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { TimeRangePicker } from "@/components/shared/TimeRangePicker";
import { Select } from "@/components/ui/select";
import { timeRangeMs, timeRangeSpan } from "@/lib/time-range";
import { AnsiText } from "@/components/logs/AnsiText";
import { openLogsAt } from "@/components/logs/logViewStore";
import { fmtClock, stripAnsi } from "@/components/logs/logParse";
import { fmtFull } from "@/components/shared/s3-TimeSeriesChart";

interface Props {
  project: string;
  service: string;
  // Switches the overlay to the Logs tab. The query + time window are
  // handed over through the logs view store before the switch.
  onViewLogs?: () => void;
}

// Log retention is 7 days, so a longer window would only look empty.
const RANGES = ["1h", "24h", "7d"] as const;

// ServiceErrorsPanel is the Sentry-style error feed: groups by
// fingerprint (GET …/errors), and per group a count histogram, the
// stack trace around the latest occurrence and the newest occurrences
// (GET …/errors/{fingerprint}).
export function ServiceErrorsPanel({ project, service, onViewLogs }: Props) {
  const [since, setSince] = useState<(typeof RANGES)[number]>("24h");
  // "" = every environment. Not pre-set from the overlay's env: error
  // events only carry an env for pods labelled kuso.sislelabs.com/env.
  const [env, setEnv] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const errors = useErrors(project, service, since, env);
  const envs = useEnvironments(project);
  const fqn = service ? project + "-" + service : "";
  const envOptions = useMemo(() => serviceEnvOptions(envs.data ?? [], fqn), [envs.data, fqn]);
  const groups = errors.data ?? [];
  const total = groups.reduce((a, g) => a + g.count, 0);

  const viewInLogs = (needle: string, at: string) => {
    const t = Date.parse(at);
    const pad = 5 * 60_000;
    openLogsAt(project, service, {
      q: needle,
      range: Number.isNaN(t)
        ? { kind: "preset", preset: since }
        : { kind: "abs", from: new Date(t - pad).toISOString(), to: new Date(t + pad).toISOString() },
      levels: [],
      pod: "",
    });
    onViewLogs?.();
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold text-[var(--text-primary)]">Errors</h3>
          <p className="text-xs text-[var(--text-secondary)]">
            {errors.isError || errors.isPending
              ? `Last ${timeRangeSpan(since)}`
              : `${groups.length} ${groups.length === 1 ? "group" : "groups"} · ${total.toLocaleString()} occurrences · last ${timeRangeSpan(since)}`}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {envOptions.length > 1 && (
            <Select
              size="sm"
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              aria-label="Environment"
              wrapperClassName="w-auto"
            >
              <option value="">All envs</option>
              {envOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
          <TimeRangePicker presets={RANGES} value={since} onChange={setSince} ariaLabel="Errors time range" />
        </div>
      </div>

      {errors.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : errors.isError ? (
        <QueryErrorState what="errors" error={errors.error} onRetry={() => void errors.refetch()} />
      ) : groups.length === 0 ? (
        <div className="rounded-lg border border-dashed border-[var(--border-subtle)] px-6 py-10 text-center">
          <p className="text-sm font-medium text-[var(--text-primary)]">No errors in the last {timeRangeSpan(since)}</p>
          <p className="mx-auto mt-1 max-w-md text-[13px] text-[var(--text-secondary)]">
            The scanner watches this service&apos;s logs for ERROR, Exception, panic and Traceback lines. Errors logged in
            another shape won&apos;t appear here. Logs are kept for 7 days.
          </p>
          {since !== "7d" && (
            <Button size="sm" variant="outline" className="mt-3" onClick={() => setSince("7d")}>
              Check the last 7 days
            </Button>
          )}
        </div>
      ) : (
        <ul className="space-y-2">
          {groups.map((g) => (
            <ErrorRow
              key={g.fingerprint}
              group={g}
              project={project}
              service={service}
              since={since}
              env={env}
              isOpen={expanded === g.fingerprint}
              onToggle={() => setExpanded(expanded === g.fingerprint ? null : g.fingerprint)}
              onViewLogs={onViewLogs ? viewInLogs : undefined}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

// logNeedle picks a search string for the logs view: the message up to
// its first variable-looking part (quoted value, id, number) so other
// occurrences of the same error match too.
export function logNeedle(sample: string): string {
  const s = stripAnsi(sample).trim();
  const cut = s.search(/["'`]|\b[0-9a-f]{8,}\b|\d{3,}/i);
  const head = (cut > 0 ? s.slice(0, cut) : s).trim().replace(/[\s:.,([{]+$/, "");
  return (head.length >= 12 ? head : s).slice(0, 80);
}

function ErrorRow({
  group,
  project,
  service,
  since,
  env,
  isOpen,
  onToggle,
  onViewLogs,
}: {
  group: ErrorGroup;
  project: string;
  service: string;
  since: string;
  env: string;
  isOpen: boolean;
  onToggle: () => void;
  onViewLogs?: (needle: string, at: string) => void;
}) {
  const panelId = `err-${group.fingerprint}`;
  return (
    <li className="overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={isOpen}
        aria-controls={panelId}
        className="flex w-full items-start gap-3 px-3.5 py-3 text-left hover:bg-[var(--bg-tertiary)]/40"
      >
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--error)]" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="line-clamp-2 break-words font-mono text-[13px] text-[var(--text-primary)]">
            {group.message || "(empty error)"}
          </div>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-[var(--text-tertiary)]">
            <span title={group.lastSeen}>Last seen {relativeTime(group.lastSeen)}</span>
            <span title={group.firstSeen}>First seen {relativeTime(group.firstSeen)}</span>
            {group.sampleEnv && <span>{group.sampleEnv}</span>}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className="text-right">
            <span className="block text-sm font-semibold tabular-nums text-[var(--text-primary)]">{group.count.toLocaleString()}</span>
            <span className="block text-xs text-[var(--text-tertiary)]">{group.count === 1 ? "event" : "events"}</span>
          </span>
          {isOpen ? (
            <ChevronDown className="h-4 w-4 text-[var(--text-tertiary)]" />
          ) : (
            <ChevronRight className="h-4 w-4 text-[var(--text-tertiary)]" />
          )}
        </div>
      </button>
      {isOpen && (
        <ErrorDetail id={panelId} group={group} project={project} service={service} since={since} env={env} onViewLogs={onViewLogs} />
      )}
    </li>
  );
}

function ErrorDetail({
  id,
  group,
  project,
  service,
  since,
  env,
  onViewLogs,
}: {
  id: string;
  group: ErrorGroup;
  project: string;
  service: string;
  since: string;
  env: string;
  onViewLogs?: (needle: string, at: string) => void;
}) {
  const detail = useQuery({
    queryKey: ["error-detail", project, service, group.fingerprint, since, env],
    queryFn: () => getErrorDetail(project, service, group.fingerprint, since, env || undefined),
    staleTime: 30_000,
  });
  const latest: ErrorOccurrence = detail.data?.occurrences[0] ?? {
    ts: group.lastSeen,
    env: group.sampleEnv,
    pod: group.samplePod,
    rawLine: group.sampleLine,
  };
  const trace = useQuery({
    queryKey: ["error-trace", project, service, latest.ts, latest.pod ?? ""],
    queryFn: () => fetchErrorTrace({ project, service }, latest),
    enabled: !detail.isPending,
    staleTime: 60_000,
  });
  // The lines this pod logged from the error line on: the stack trace the
  // scanner's one-line sample cuts off.
  const traceLines = useMemo(() => {
    const lines = trace.data ?? [];
    const raw = stripAnsi(latest.rawLine);
    const start = lines.findIndex((l) => stripAnsi(l.line) === raw);
    const from = start >= 0 ? lines.slice(start) : [];
    return from.slice(0, 80);
  }, [trace.data, latest.rawLine]);

  const needle = logNeedle(group.sampleLine || group.message);

  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(`Copied ${what}`);
    } catch {
      toast.error("Copy failed — clipboard unavailable");
    }
  };

  return (
    <div id={id} className="space-y-4 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)] p-4">
      <section>
        <div className="mb-1.5 flex items-baseline justify-between gap-2">
          <h4 className="text-xs font-medium text-[var(--text-secondary)]">Occurrences · last {timeRangeSpan(since)}</h4>
          {detail.data === null && <span className="text-xs text-[var(--text-tertiary)]">history needs a newer kuso server</span>}
        </div>
        {detail.isPending ? (
          <Skeleton className="h-16 w-full" />
        ) : detail.data ? (
          <OccurrenceHistogram detail={detail.data} since={since} />
        ) : detail.isError ? (
          <p className="text-xs text-[var(--error)]">Couldn&apos;t load occurrence history.</p>
        ) : null}
      </section>

      <section>
        <div className="mb-1.5 flex flex-wrap items-baseline justify-between gap-2">
          <h4 className="text-xs font-medium text-[var(--text-secondary)]">
            {traceLines.length > 1 ? "Stack trace" : "Sample"}
            <span className="ml-1.5 font-normal text-[var(--text-tertiary)]">
              latest · {fmtClock(latest.ts, true).slice(0, -4)}
              {latest.pod ? ` · ${latest.pod.split("-").slice(-2).join("-")}` : ""}
            </span>
          </h4>
          <button
            type="button"
            onClick={() => void copy(traceLines.length > 1 ? traceLines.map((l) => stripAnsi(l.line)).join("\n") : stripAnsi(latest.rawLine), "trace")}
            className="inline-flex items-center gap-1 text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
          >
            <Copy className="h-3 w-3" /> Copy
          </button>
        </div>
        <pre className="max-h-80 overflow-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 font-mono text-[12px] leading-relaxed text-[var(--text-primary)] whitespace-pre-wrap [overflow-wrap:anywhere]">
          {traceLines.length > 1
            ? traceLines.map((l, i) => (
                <div key={l.id} className={cn(i > 0 && "text-[var(--text-secondary)]")}>
                  <AnsiText text={l.line} />
                </div>
              ))
            : <AnsiText text={latest.rawLine} />}
        </pre>
        {trace.isFetching && <p className="mt-1 text-xs text-[var(--text-tertiary)]">Looking for the full trace in the archive…</p>}
      </section>

      {detail.data && detail.data.occurrences.length > 0 && (
        <section>
          <h4 className="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">Recent events</h4>
          <ul className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-md border border-[var(--border-subtle)]">
            {detail.data.occurrences.slice(0, 10).map((o, i) => (
              <li key={`${o.ts}-${i}`} className="flex items-center gap-3 px-3 py-1.5 text-xs">
                <span className="w-36 shrink-0 tabular-nums text-[var(--text-secondary)]" title={o.ts}>
                  {fmtClock(o.ts, true).slice(0, -4)}
                </span>
                <span className="min-w-0 flex-1 truncate font-mono text-[var(--text-tertiary)]" title={o.pod}>
                  {o.env ? `${o.env} · ` : ""}
                  {o.pod ? o.pod.split("-").slice(-2).join("-") : ""}
                </span>
                {onViewLogs && (
                  <button
                    type="button"
                    onClick={() => onViewLogs(needle, o.ts)}
                    className="shrink-0 text-[var(--text-secondary)] underline-offset-2 hover:text-[var(--text-primary)] hover:underline"
                  >
                    Logs
                  </button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {onViewLogs && (
          <Button size="sm" variant="outline" onClick={() => onViewLogs(needle, latest.ts)}>
            <ScrollText className="h-3.5 w-3.5" /> View in logs
          </Button>
        )}
        <span className="min-w-0 truncate text-xs text-[var(--text-tertiary)]">
          Opens logs around the latest event, searching “{needle}”
        </span>
        <button
          type="button"
          onClick={() => void copy(group.fingerprint, "fingerprint")}
          className="ml-auto font-mono text-xs text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
          title="Copy fingerprint"
        >
          #{group.fingerprint}
        </button>
      </div>
    </div>
  );
}

// OccurrenceHistogram: event counts per bucket across the window, one
// column per bucket, with a per-bar hover/focus readout.
function OccurrenceHistogram({ detail, since }: { detail: ErrorGroupDetail; since: string }) {
  const [hover, setHover] = useState<number | null>(null);
  const bucketMs = detail.bucketSeconds * 1000;
  const end = Date.now();
  const start = end - (timeRangeMs(since) ?? 0);
  const first = Math.floor(start / bucketMs) * bucketMs;
  const byT = new Map(detail.buckets.map(([t, c]) => [t * 1000, c]));
  const cols: { t: number; c: number }[] = [];
  for (let t = first; t <= end; t += bucketMs) cols.push({ t, c: byT.get(t) ?? 0 });
  const max = Math.max(1, ...cols.map((c) => c.c));
  const active = hover !== null ? cols[hover] : null;
  return (
    <div>
      <div
        className="relative flex h-14 items-end gap-[2px]"
        role="img"
        aria-label={`${detail.buckets.reduce((a, b) => a + b[1], 0)} events in the last ${timeRangeSpan(since)}, peak ${max} per bucket`}
        onPointerLeave={() => setHover(null)}
      >
        {cols.map((col, i) => (
          <div key={col.t} className="flex h-full min-w-0 flex-1 items-end" onPointerEnter={() => setHover(i)}>
            <div
              className={cn(
                "w-full rounded-t-[2px]",
                col.c > 0 ? "bg-[var(--error)]" : "bg-[var(--border-subtle)]",
                hover === i && "opacity-80",
              )}
              style={{ height: col.c > 0 ? `${Math.max(8, (col.c / max) * 100)}%` : "2px" }}
            />
          </div>
        ))}
        {active && (
          <div
            className="pointer-events-none absolute -top-1 z-10 -translate-y-full rounded-md border border-[var(--border-default)] bg-[var(--bg-elevated)] px-2 py-1 text-xs shadow-[var(--shadow-md)]"
            style={hover! > cols.length / 2 ? { right: `${((cols.length - hover! - 1) / cols.length) * 100}%` } : { left: `${(hover! / cols.length) * 100}%` }}
          >
            <span className="font-semibold tabular-nums text-[var(--text-primary)]">{active.c}</span>
            <span className="ml-1 text-[var(--text-tertiary)]">{fmtFull(active.t)}</span>
          </div>
        )}
      </div>
      <div className="mt-1 flex justify-between text-xs tabular-nums text-[var(--text-tertiary)]">
        <span>{fmtFull(first).replace(/:\d\d$/, "")}</span>
        <span>now</span>
      </div>
    </div>
  );
}
