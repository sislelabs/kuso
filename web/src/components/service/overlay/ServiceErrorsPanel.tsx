"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import Link from "next/link";
import { AlertTriangle, BellPlus, CheckCircle2, ChevronDown, ChevronRight, Copy, GitCommitHorizontal, ScrollText } from "lucide-react";
import { useBuilds, useErrors } from "@/features/services";
import type { BuildSummary, ErrorGroup } from "@/features/services";
import { useEnvironments } from "@/features/projects";
import { errorTitle, sinceDeployWindow, useResolveError, useServiceErrorCount } from "@/features/errors";
import { lastDeployedAt } from "@/features/projects/facts";
import { ApiError } from "@/lib/api-client";
import { toastError } from "@/lib/errors";
import { fetchErrorTrace, getErrorDetail } from "@/features/logs";
import type { ErrorGroupDetail, ErrorOccurrence } from "@/features/logs";
import { envGroupName, serviceEnvOptions } from "@/lib/env-group";
import { relativeTime, shortSha } from "@/lib/format";
import { unavailableNotice } from "@/features/system-version";
import { cn } from "@/lib/utils";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { TimeRangePicker } from "@/components/shared/TimeRangePicker";
import { Select } from "@/components/ui/select";
import { timeRangeMs, timeRangeSpan } from "@/lib/time-range";
import { AnsiText } from "@/components/logs/AnsiText";
import { openLogsAt } from "@/components/logs/logViewStore";
import { fmtClock, stripAnsi } from "@/components/logs/logParse";
import { buildLiveAt, plural, traceFrames } from "@/components/logs/errorInsights";
import { fmtFull } from "@/components/shared/s3-TimeSeriesChart";

interface Props {
  project: string;
  service: string;
  // Switches the overlay to the Logs tab. The query + time window are
  // handed over through the logs view store before the switch.
  onViewLogs?: () => void;
  // Env group the overlay is showing; the panel starts filtered to it.
  defaultEnv?: string;
}

// Log retention is 7 days, so a longer window would only look empty.
const RANGES = ["1h", "24h", "7d"] as const;

// ServiceErrorsPanel is the Sentry-style error feed: groups by
// fingerprint (GET …/errors), and per group a count histogram, the
// stack trace around the latest occurrence and the newest occurrences
// (GET …/errors/{fingerprint}).
export function ServiceErrorsPanel({ project, service, onViewLogs, defaultEnv = "" }: Props) {
  const [since, setSince] = useState<(typeof RANGES)[number]>("24h");
  // Starts on the overlay's env; "" = every environment. Events from pods
  // without the kuso.sislelabs.com/env label only show under "All".
  const [pickedEnv, setEnv] = useState(defaultEnv);
  const [expanded, setExpanded] = useState<string | null>(null);
  const envs = useEnvironments(project);
  const fqn = service ? project + "-" + service : "";
  const envOptions = useMemo(() => serviceEnvOptions(envs.data ?? [], fqn), [envs.data, fqn]);
  // With one environment there's nothing to narrow to, and filtering
  // would hide events from unlabelled pods.
  const env = envOptions.length < 2 ? "" : pickedEnv;
  // "Since current deploy" narrows the window to the env's live version,
  // so a fixed error stops counting once the fix is deployed.
  const [sinceDeploy, setSinceDeploy] = useState(false);
  const deployedAt = useMemo(() => {
    const group = env || "production";
    return lastDeployedAt((envs.data ?? []).find((e) => e.spec?.service === fqn && envGroupName(e) === group));
  }, [envs.data, fqn, env]);
  const [deployWindow, setDeployWindow] = useState<string | null>(null);
  useEffect(() => {
    setDeployWindow(sinceDeployWindow(deployedAt, Date.now()));
  }, [deployedAt, sinceDeploy]);
  const range = sinceDeploy && deployWindow ? deployWindow : since;
  const rangeWords = sinceDeploy && deployWindow ? "since the current deploy" : `in the last ${timeRangeSpan(since)}`;
  const errors = useErrors(project, service, range, env);
  const groups = errors.data ?? [];
  // The headline total comes from the same summary as the canvas badge and
  // the tab badge, so the three agree even when the list is cut at 50.
  const count = useServiceErrorCount(project, service, range, env);
  const total = count.data?.occurrences ?? groups.reduce((a, g) => a + g.count, 0);
  const groupCount = count.data?.groups ?? groups.length;
  const builds = useBuilds(project, service);
  const windowStart = Date.now() - (timeRangeMs(range) ?? 0);
  const announcement = useNewErrorAnnouncement(errors.isSuccess ? total : null, `${range}/${env}`);

  // ±30s around the occurrence: tight enough that the error's own lines
  // are on screen, wide enough for the request that caused it.
  const viewInLogs = (needle: string, at: string) => {
    const t = Date.parse(at);
    const pad = 30_000;
    openLogsAt(project, service, {
      q: needle,
      range: Number.isNaN(t)
        ? { kind: "preset", preset: since }
        : { kind: "abs", from: new Date(t - pad).toISOString(), to: new Date(t + pad).toISOString() },
      levels: [],
      pod: "",
      // A Logs view left on another env would hide the occurrence.
      env,
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
              ? rangeWords.charAt(0).toUpperCase() + rangeWords.slice(1)
              : `${plural(total, "error")} ${rangeWords} · ${plural(groupCount, "group")}${groupCount > groups.length ? ` · newest ${groups.length} shown` : ""}`}
          </p>
          <div role="status" aria-live="polite" aria-atomic="true" className="sr-only">
            {announcement}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/* Always shown so every service's Errors tab has the same
              controls; with one env there's nothing to pick. */}
          {envOptions.length > 0 && (
            <Select
              size="sm"
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              aria-label="Environment"
              wrapperClassName="w-auto"
              disabled={envOptions.length < 2}
              title={envOptions.length < 2 ? `${service} only runs in ${envOptions[0]?.label ?? "one environment"}` : undefined}
            >
              <option value="">All environments</option>
              {envOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
          <button
            type="button"
            aria-pressed={sinceDeploy}
            disabled={!deployedAt}
            onClick={() => setSinceDeploy((v) => !v)}
            title={
              deployedAt
                ? `Only errors since ${service} was last deployed (${relativeTime(deployedAt)})`
                : "No deploy recorded for this environment"
            }
            className={cn(
              "h-8 rounded-md border px-2.5 text-xs disabled:opacity-50",
              sinceDeploy
                ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                : "border-[var(--border-subtle)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
            )}
          >
            Since current deploy
          </button>
          <TimeRangePicker
            presets={RANGES}
            value={since}
            onChange={(v) => {
              setSince(v);
              setSinceDeploy(false);
            }}
            ariaLabel="Errors time range"
          />
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
          <p className="text-sm font-medium text-[var(--text-primary)]">No unresolved errors {rangeWords}</p>
          <p className="mx-auto mt-1 max-w-md text-[13px] text-[var(--text-secondary)]">
            The scanner watches this service&apos;s runtime logs for ERROR, Exception, panic and Traceback lines. Build and
            release-hook output isn&apos;t counted (it&apos;s on Deployments), and errors logged in another shape won&apos;t
            appear here. Logs are kept for 7 days.
          </p>
          {(since !== "7d" || sinceDeploy) && (
            <Button
              size="sm"
              variant="outline"
              className="mt-3"
              onClick={() => {
                setSince("7d");
                setSinceDeploy(false);
              }}
            >
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
              since={range}
              env={env}
              firstBuild={buildLiveAt(builds.data ?? [], g.firstSeen, windowStart)}
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

function useInView(ref: React.RefObject<HTMLElement | null>): boolean {
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el || seen) return;
    if (typeof IntersectionObserver === "undefined") {
      setSeen(true);
      return;
    }
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) setSeen(true);
    });
    io.observe(el);
    return () => io.disconnect();
  }, [ref, seen]);
  return seen;
}

// useNewErrorAnnouncement says "N new errors" when a background refetch
// raises the occurrence total; a filter change only resets the baseline.
function useNewErrorAnnouncement(total: number | null, scope: string): string {
  const [msg, setMsg] = useState("");
  const base = useRef<{ scope: string; total: number } | null>(null);
  useEffect(() => {
    if (total === null) return;
    const prev = base.current;
    if (prev && prev.scope === scope && total > prev.total) {
      const d = total - prev.total;
      setMsg(`${plural(d, "new error occurrence")}`);
    }
    base.current = { scope, total };
  }, [total, scope]);
  return msg;
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
  firstBuild,
  isOpen,
  onToggle,
  onViewLogs,
}: {
  group: ErrorGroup;
  project: string;
  service: string;
  since: string;
  env: string;
  firstBuild: { build: BuildSummary; afterMs: number } | null;
  isOpen: boolean;
  onToggle: () => void;
  onViewLogs?: (needle: string, at: string) => void;
}) {
  const panelId = `err-${group.fingerprint}`;
  const liRef = useRef<HTMLLIElement>(null);
  const inView = useInView(liRef);
  // Same query key as the expanded detail's trace, so expanding reuses it.
  const trace = useQuery({
    queryKey: ["error-trace", project, service, group.lastSeen, group.samplePod ?? ""],
    queryFn: () => fetchErrorTrace({ project, service }, { ts: group.lastSeen, pod: group.samplePod, env: group.sampleEnv }),
    enabled: inView && !!group.sampleLine,
    staleTime: 5 * 60_000,
  });
  const frames = useMemo(
    () => traceFrames((trace.data ?? []).map((l) => l.line), group.sampleLine),
    [trace.data, group.sampleLine],
  );
  const fb = firstBuild?.build;
  const fbLabel = fb ? (fb.commitSha ? shortSha(fb.commitSha) : (fb.ref ?? fb.id)) : "";
  const resolve = useResolveError(project, service);
  const onResolve = () =>
    resolve.mutate(
      { fingerprint: group.fingerprint },
      {
        onSuccess: () => toast.success("Resolved", { description: "Hidden until it happens again." }),
        onError: (e) =>
          e instanceof ApiError && (e.status === 404 || e.status === 405)
            ? toast.error("This server can't resolve errors yet")
            : toastError(e, "Couldn't resolve"),
      },
    );
  return (
    <li ref={liRef} className="overflow-hidden rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <div className="flex items-stretch">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={isOpen}
        aria-controls={panelId}
        className="flex min-w-0 flex-1 items-start gap-3 px-3.5 py-3 text-left hover:bg-[var(--bg-tertiary)]/40"
      >
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--error)]" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="line-clamp-2 break-words font-mono text-[13px] text-[var(--text-primary)]" title={group.message}>
            {group.message ? errorTitle(group.message) : "(empty error)"}
          </div>
          <div className="mt-0.5 text-xs text-[var(--text-secondary)] sm:hidden">
            <span className="font-semibold tabular-nums text-[var(--text-primary)]">{group.count.toLocaleString()}</span>{" "}
            {group.count === 1 ? "occurrence" : "occurrences"}
          </div>
          {frames.length > 0 && !isOpen && (
            <div className="mt-1 space-y-px border-l-2 border-[var(--border-default)] pl-2 font-mono text-xs text-[var(--text-tertiary)]">
              {frames.map((f, i) => (
                <div key={i} className="truncate" title={f}>
                  {f}
                </div>
              ))}
            </div>
          )}
          <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-[var(--text-tertiary)]">
            <span title={group.lastSeen}>Last seen {relativeTime(group.lastSeen)}</span>
            <span title={group.firstSeen}>First seen {relativeTime(group.firstSeen)}</span>
            {fb && firstBuild && (
              <span
                className="inline-flex items-center gap-1"
                title={`${fb.commitMessage?.split("\n")[0] ?? "Build " + fbLabel} — the build that was live when this error first appeared in the last ${timeRangeSpan(since)}`}
              >
                <GitCommitHorizontal aria-hidden className="h-3.5 w-3.5" />
                in build <span className="font-mono text-[var(--text-secondary)]">{fbLabel}</span>
                {firstBuild.afterMs < 3_600_000 && (
                  <span className="text-[var(--warning)]">· {Math.max(1, Math.round(firstBuild.afterMs / 60_000))}m after deploy</span>
                )}
              </span>
            )}
            {group.sampleEnv && <span>{group.sampleEnv}</span>}
            {group.resolvedAt && (
              <span className="text-[var(--warning)]" title={`Resolved ${new Date(group.resolvedAt).toLocaleString()}`}>
                Recurred after being resolved {relativeTime(group.resolvedAt)}
              </span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className="hidden text-right sm:block">
            <span className="block text-sm font-semibold tabular-nums text-[var(--text-primary)]">{group.count.toLocaleString()}</span>
            <span className="block text-xs text-[var(--text-tertiary)]">{group.count === 1 ? "occurrence" : "occurrences"}</span>
          </span>
          {isOpen ? (
            <ChevronDown className="h-4 w-4 text-[var(--text-tertiary)]" />
          ) : (
            <ChevronRight className="h-4 w-4 text-[var(--text-tertiary)]" />
          )}
        </div>
      </button>
      <button
        type="button"
        onClick={onResolve}
        disabled={resolve.isPending}
        title="Resolve: hide this group until it happens again"
        className="flex shrink-0 items-center gap-1 border-l border-[var(--border-subtle)] px-3 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)]/40 hover:text-[var(--text-primary)] disabled:opacity-50"
      >
        <CheckCircle2 aria-hidden className="h-3.5 w-3.5" />
        <span className="hidden sm:inline">Resolve</span>
        <span className="sr-only sm:hidden">Resolve</span>
      </button>
      </div>
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

  const needle = logNeedle(errorTitle(stripAnsi(group.sampleLine || group.message)));

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
          {detail.data === null && <span className="text-xs text-[var(--text-tertiary)]">{unavailableNotice("History")}</span>}
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
          <h4 className="mb-1.5 text-xs font-medium text-[var(--text-secondary)]">Recent occurrences</h4>
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
                    aria-label={`View in logs at ${fmtClock(o.ts, false).slice(0, 8)}`}
                    className="shrink-0 text-[var(--text-secondary)] underline-offset-2 hover:text-[var(--text-primary)] hover:underline"
                  >
                    View in logs
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
            <ScrollText className="h-3.5 w-3.5" /> View in logs at {fmtClock(latest.ts, false).slice(0, 8)}
          </Button>
        )}
        {onViewLogs && (
          <span className="min-w-0 truncate text-xs text-[var(--text-tertiary)]" title={`Searches “${needle}”`}>
            Opens the Logs tab 30s either side of the latest occurrence
          </span>
        )}
        <Link
          href={`/settings/alerts?${new URLSearchParams({ new: "1", kind: "log_match", project, service, query: needle }).toString()}`}
          className={buttonVariants({ size: "sm", variant: "outline" })}
          title={`New log-match alert rule for “${needle}”`}
        >
          <BellPlus className="h-3.5 w-3.5" /> Alert on this
        </Link>
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
        aria-label={`${plural(detail.buckets.reduce((a, b) => a + b[1], 0), "occurrence")} in the last ${timeRangeSpan(since)}, peak ${max} per bucket`}
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
