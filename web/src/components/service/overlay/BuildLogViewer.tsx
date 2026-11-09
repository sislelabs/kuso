"use client";

import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowDown,
  Ban,
  Check,
  ChevronRight,
  CircleDashed,
  Download,
  Layers,
  Loader2,
  RefreshCw,
  Search,
  WrapText,
  X,
} from "lucide-react";
import {
  deployStage,
  formatStepDuration,
  parseBuildLog,
  useLiveBuildLog,
  useStoredBuildLog,
  type BuildStep,
  type DeployStage,
  type LiveLogState,
  type StepStatus,
} from "@/features/builds";
import { AnsiText } from "@/components/logs/AnsiText";
import { buildMatcher } from "@/components/logs/logParse";
import { ApiError } from "@/lib/api-client";
import { describeError } from "@/lib/errors";
import { cn } from "@/lib/utils";
import { CopyButton } from "@/components/ui/copy-button";
import { Segmented } from "@/components/ui/segmented";

type View = "steps" | "raw";

// BuildLogViewer shows one build's log. Running builds stream over the
// WebSocket (with reconnect); finished builds read the stored log once.
// The default view folds BuildKit output into steps with status and
// duration; a failed build opens on the failing step with the error lines
// marked. "Raw" is the plain log.
export const BuildLogViewer = memo(function BuildLogViewer({
  project,
  service,
  buildId,
  live,
  failed,
  onStage,
  jumpTo,
  className,
}: {
  project: string;
  service: string;
  buildId: string;
  live: boolean;
  failed: boolean;
  // Scrolls the raw log to this 1-based line and marks it; seq re-triggers
  // a jump to the same line.
  jumpTo?: { line: number; seq: number };
  // Reports how far a running build has got (first-deploy progress).
  onStage?: (stage: DeployStage) => void;
  className?: string;
}) {
  const stored = useStoredBuildLog(project, service, buildId, !live);
  const stream = useLiveBuildLog(project, service, buildId, live);

  const lines = useMemo(
    () => (live ? stream.lines.map((l) => l.line) : (stored.data ?? []).map((l) => l.line)),
    [live, stream.lines, stored.data],
  );
  const parsed = useMemo(() => parseBuildLog(lines, { live, failed }), [lines, live, failed]);

  const stage = deployStage(parsed);
  useEffect(() => {
    if (live) onStage?.(stage);
  }, [live, stage, onStage]);

  const [viewPref, setView] = useState<View>("steps");
  const view: View = parsed.structured ? viewPref : "raw";
  const [filter, setFilter] = useState("");
  const matcher = useMemo(() => buildMatcher(filter), [filter]);
  const [wrap, setWrap] = useState(true);

  const scroller = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const [marked, setMarked] = useState(0);
  const jumpLine = jumpTo?.line ?? 0;
  const jumpSeq = jumpTo?.seq ?? 0;
  useEffect(() => {
    if (!jumpLine) return;
    setView("raw");
    setFilter("");
    setMarked(jumpLine);
  }, [jumpLine, jumpSeq]);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!marked || !el) return;
    const row = el.querySelector<HTMLElement>(`[data-line="${marked}"]`);
    if (row) el.scrollTop = Math.max(0, row.offsetTop - el.clientHeight / 2);
  }, [marked, jumpSeq, viewPref, lines.length]);
  useEffect(() => {
    if (!live || !follow) return;
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines, live, follow, view]);

  const loading = live ? stream.state === "connecting" && lines.length === 0 : stored.isPending;
  // A 404 is final (the log expired or was never archived); anything else
  // (network, 5xx) may pass on a second try, so only that gets Retry.
  const loadError: { message: string; detail?: string; retry: boolean } | null =
    !live && stored.isError
      ? stored.error instanceof ApiError && stored.error.status === 404
        ? { message: stored.error.message || "This build's log is no longer available.", retry: false }
        : { message: "Couldn't load the build log.", detail: describeError(stored.error, "The request failed."), retry: true }
      : null;

  const download = () => {
    const blob = new Blob([lines.join("\n") + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${buildId}.log`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const rawShown = useMemo(() => {
    const all = lines.map((text, idx) => ({ text, idx }));
    return matcher ? all.filter((l) => matcher.test(l.text)) : all;
  }, [lines, matcher]);

  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  const summary = matcher
    ? `${plural(rawShown.length, "matching line")}`
    : view === "steps" && parsed.structured
      ? plural(parsed.steps.length, "step")
      : plural(lines.length, "line");

  return (
    <div className={cn("flex min-w-0 flex-col", className)}>
      <div className="flex flex-wrap items-center gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5">
        {parsed.structured && (
          <Segmented
            size="sm"
            label="Log view"
            value={view}
            onChange={setView}
            options={[
              { value: "steps", label: "Steps", title: "BuildKit steps, folded" },
              { value: "raw", label: "Raw", title: "The log as the build printed it" },
            ]}
          />
        )}
        {live ? (
          <StreamState
            state={stream.state}
            message={stream.message}
            onReconnect={stream.reconnect}
            empty={stream.lines.length === 0}
          />
        ) : (
          stored.data && <span className="text-xs text-[var(--text-secondary)]">{summary}</span>
        )}
        <div className="ml-auto flex items-center gap-1">
          <label className="relative">
            <span className="sr-only">Search the build log</span>
            <Search className="pointer-events-none absolute left-1.5 top-1/2 h-3 w-3 -translate-y-1/2 text-[var(--text-tertiary)]" />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Search"
              spellCheck={false}
              className="h-7 w-28 rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] pl-5 pr-1.5 text-xs text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus:border-[var(--border-strong)] sm:w-40"
            />
          </label>
          {view === "raw" && (
            <button
              type="button"
              aria-pressed={wrap}
              onClick={() => setWrap((w) => !w)}
              title={wrap ? "Don't wrap lines" : "Wrap lines"}
              aria-label="Wrap lines"
              className={cn(
                "inline-flex h-7 w-7 items-center justify-center rounded hover:bg-[var(--bg-tertiary)]",
                wrap ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]",
              )}
            >
              <WrapText className="h-3.5 w-3.5" />
            </button>
          )}
          <CopyButton value={lines.join("\n")} label="Copy log" />
          <button
            type="button"
            onClick={download}
            disabled={lines.length === 0}
            title="Download .log"
            aria-label="Download log"
            className="inline-flex h-7 w-7 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-40"
          >
            <Download className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      <div className="relative">
        <div
          ref={scroller}
          onScroll={() => {
            const el = scroller.current;
            if (!el || !live) return;
            const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
            if (atBottom !== follow) setFollow(atBottom);
          }}
          className="dark relative max-h-[480px] min-h-[96px] overflow-auto bg-[var(--console-bg)] font-mono text-xs leading-[1.6] text-[var(--console-fg)]"
          role="log"
          aria-live={live ? "polite" : undefined}
          aria-label="Build log"
        >
          {loading ? (
            <p className="px-3 py-2 text-[var(--text-tertiary)]">Loading log…</p>
          ) : loadError ? (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2 font-sans text-[var(--text-secondary)]">
              <span className="min-w-0">
                {loadError.message}
                {loadError.detail && <span className="text-[var(--text-tertiary)]"> {loadError.detail}</span>}
              </span>
              {loadError.retry && (
                <button
                  type="button"
                  onClick={() => void stored.refetch()}
                  disabled={stored.isFetching}
                  className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--border-default)] px-2 text-xs text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] disabled:opacity-50"
                >
                  <RefreshCw aria-hidden className={cn("h-3.5 w-3.5", stored.isFetching && "motion-safe:animate-spin")} />
                  {stored.isFetching ? "Retrying…" : "Retry"}
                </button>
              )}
            </div>
          ) : lines.length === 0 ? (
            <p className="px-3 py-2 text-[var(--text-tertiary)]">
              {live ? "Waiting for the build to print…" : "The build printed nothing."}
            </p>
          ) : view === "steps" ? (
            <StepList
              steps={parsed.steps}
              truncated={parsed.truncated}
              failedStepId={parsed.failedStepId}
              live={live}
              matcher={matcher}
            />
          ) : rawShown.length === 0 ? (
            <p className="px-3 py-2 text-[var(--text-tertiary)]">No lines match “{filter}”.</p>
          ) : (
            <ol className="py-2">
              {rawShown.map((l) => (
                <li
                  key={l.idx}
                  data-line={l.idx + 1}
                  aria-current={l.idx + 1 === marked ? "true" : undefined}
                  className={cn(
                    "flex gap-3 border-l-2 px-3",
                    l.idx + 1 === marked ? "border-[var(--danger)] bg-[var(--danger)]/10" : "border-transparent",
                  )}
                >
                  <span aria-hidden className="w-8 shrink-0 select-none text-right text-[var(--text-tertiary)]">
                    {l.idx + 1}
                  </span>
                  <span className={cn("min-w-0", wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre")}>
                    <AnsiText text={l.text} highlight={matcher?.re} />
                  </span>
                </li>
              ))}
            </ol>
          )}
        </div>
        {live && !follow && (
          <button
            type="button"
            onClick={() => setFollow(true)}
            className="absolute bottom-2 left-1/2 inline-flex -translate-x-1/2 items-center gap-1 rounded-full border border-[var(--border-strong)] bg-[var(--bg-elevated)] px-3 py-1 text-xs text-[var(--text-primary)] shadow-[var(--shadow-md)]"
          >
            <ArrowDown className="h-3 w-3" />
            Jump to latest
          </button>
        )}
      </div>
    </div>
  );
});

type Row = { type: "step"; step: BuildStep } | { type: "minor"; steps: BuildStep[] };

// Runs of two or more bookkeeping steps (cache hits, metadata loads) fold
// into one row so the steps that did real work stand out.
function toRows(steps: BuildStep[]): Row[] {
  const rows: Row[] = [];
  let run: BuildStep[] = [];
  const flush = () => {
    if (run.length >= 2) rows.push({ type: "minor", steps: run });
    else run.forEach((s) => rows.push({ type: "step", step: s }));
    run = [];
  };
  for (const s of steps) {
    if (s.minor) run.push(s);
    else {
      flush();
      rows.push({ type: "step", step: s });
    }
  }
  flush();
  return rows;
}

function StepList({
  steps,
  truncated,
  failedStepId,
  live,
  matcher,
}: {
  steps: BuildStep[];
  truncated: boolean;
  failedStepId?: string;
  live: boolean;
  matcher: ReturnType<typeof buildMatcher>;
}) {
  // User toggles win; otherwise failing, error-detail and running steps
  // start open.
  const [toggled, setToggled] = useState<Record<string, boolean>>({});
  const isOpen = (s: BuildStep) =>
    toggled[s.id] ?? (s.id === failedStepId || (s.kind === "summary" && s.status === "error") || s.status === "running");

  const visible = useMemo(() => {
    if (!matcher) return steps;
    return steps.filter((s) => matcher.test(s.title) || s.lines.some((l) => matcher.test(l.text)));
  }, [steps, matcher]);
  const rows = useMemo(() => (matcher ? visible.map((step) => ({ type: "step", step }) as Row) : toRows(visible)), [visible, matcher]);

  // Land on the failure once: scroll the console (not the page) so the
  // first error line of the failing step sits a third of the way down.
  const focusRef = useRef<HTMLElement | null>(null);
  const landed = useRef(false);
  useLayoutEffect(() => {
    if (landed.current || live || !failedStepId) return;
    const el = focusRef.current;
    // The console's own ref isn't attached yet when this child effect
    // runs (refs attach child-first), so find it from the line.
    const box = el?.closest<HTMLElement>('[role="log"]');
    if (!el || !box) return;
    landed.current = true;
    box.scrollTop = Math.max(0, el.offsetTop - box.clientHeight / 3);
  });

  if (visible.length === 0) {
    return <p className="px-3 py-2 text-[var(--text-tertiary)]">No steps match the search.</p>;
  }
  const failedStep = steps.find((s) => s.id === failedStepId);
  const firstError = failedStep?.lines.find((l) => l.error) ?? failedStep?.lines[failedStep.lines.length - 1];

  return (
    <div className="py-1">
      {truncated && (
        <p className="border-b border-[var(--border-subtle)] px-3 pb-2 pt-1 font-sans text-xs text-[var(--text-tertiary)]">
          Only the end of this build&apos;s log was kept, so earlier steps aren&apos;t shown.
        </p>
      )}
      <ul>
        {rows.map((row) =>
          row.type === "minor" ? (
            <MinorRun
              key={`minor-${row.steps[0].id}`}
              steps={row.steps}
              open={toggled[`minor-${row.steps[0].id}`] ?? false}
              onToggle={() =>
                setToggled((t) => ({ ...t, [`minor-${row.steps[0].id}`]: !(t[`minor-${row.steps[0].id}`] ?? false) }))
              }
              matcher={matcher}
            />
          ) : (
            <StepRow
              key={row.step.id}
              step={row.step}
              open={!!matcher || isOpen(row.step)}
              onToggle={() => setToggled((t) => ({ ...t, [row.step.id]: !isOpen(row.step) }))}
              matcher={matcher}
              focusIdx={row.step.id === failedStepId ? firstError?.idx : undefined}
              focusRef={focusRef}
            />
          ),
        )}
      </ul>
    </div>
  );
}

function MinorRun({
  steps,
  open,
  onToggle,
  matcher,
}: {
  steps: BuildStep[];
  open: boolean;
  onToggle: () => void;
  matcher: ReturnType<typeof buildMatcher>;
}) {
  const cached = steps.filter((s) => s.status === "cached").length;
  const total = steps.reduce((sum, s) => sum + (s.durationS ?? 0), 0);
  const label =
    cached === steps.length
      ? `${steps.length} cached steps`
      : cached > 0
        ? `${steps.length} setup steps · ${cached} cached`
        : `${steps.length} setup steps`;
  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-left font-sans text-[var(--text-tertiary)] hover:bg-[var(--console-bg-hover)] hover:text-[var(--text-secondary)]"
      >
        <ChevronRight aria-hidden className={cn("h-3.5 w-3.5 shrink-0 transition-transform", open && "rotate-90")} />
        <Layers aria-hidden className="h-3.5 w-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{label}</span>
        <span className="shrink-0 tabular-nums">{formatStepDuration(total)}</span>
      </button>
      {open && (
        <ul className="ml-5 border-l border-[var(--border-subtle)]">
          {steps.map((s) => (
            <StepRow key={s.id} step={s} open={false} onToggle={() => {}} matcher={matcher} compact />
          ))}
        </ul>
      )}
    </li>
  );
}

const STEP_ICON: Record<StepStatus, { icon: React.ComponentType<{ className?: string }>; tone: string; label: string }> = {
  done: { icon: Check, tone: "text-[var(--success)]", label: "Done" },
  cached: { icon: Layers, tone: "text-[var(--text-tertiary)]", label: "Cached" },
  error: { icon: X, tone: "text-[var(--danger)]", label: "Failed" },
  canceled: { icon: Ban, tone: "text-[var(--text-tertiary)]", label: "Cancelled" },
  running: { icon: Loader2, tone: "text-[var(--building)] motion-safe:animate-spin", label: "Running" },
  unknown: { icon: CircleDashed, tone: "text-[var(--text-tertiary)]", label: "No result recorded" },
};

// Dockerfile instructions read as code; prose titles ("Clone repository")
// stay in the UI font.
const INSTRUCTION = /^(RUN|COPY|ADD|FROM|WORKDIR|ENV|ARG|USER|EXPOSE|CMD|ENTRYPOINT|LABEL|VOLUME|SHELL|HEALTHCHECK)\b/;

function StepRow({
  step: s,
  open,
  onToggle,
  matcher,
  focusIdx,
  focusRef,
  compact,
}: {
  step: BuildStep;
  open: boolean;
  onToggle: () => void;
  matcher: ReturnType<typeof buildMatcher>;
  focusIdx?: number;
  focusRef?: React.MutableRefObject<HTMLElement | null>;
  compact?: boolean;
}) {
  const meta = STEP_ICON[s.status];
  const Icon = meta.icon;
  const expandable = !compact && (s.lines.length > 0 || !!s.errorMessage);
  const failed = s.status === "error";
  const header = (
    <>
      {!compact && (
        <ChevronRight
          aria-hidden
          className={cn("h-3.5 w-3.5 shrink-0 transition-transform", open && "rotate-90", !expandable && "invisible")}
        />
      )}
      <Icon aria-hidden className={cn("h-3.5 w-3.5 shrink-0", meta.tone)} />
      <span className="sr-only">{meta.label}:</span>
      <span
        className={cn(
          "min-w-0 flex-1 truncate",
          INSTRUCTION.test(s.title) ? "font-mono" : "font-sans",
          failed ? "text-[var(--danger)]" : s.status === "cached" || s.status === "canceled" ? "text-[var(--text-tertiary)]" : "text-[var(--console-fg)]",
        )}
        title={s.title}
      >
        <AnsiText text={s.title} highlight={matcher?.re} />
      </span>
      {s.detail && (
        <span className="hidden shrink-0 font-sans text-[var(--text-tertiary)] sm:inline">{s.detail}</span>
      )}
      <span className="w-14 shrink-0 text-right font-sans tabular-nums text-[var(--text-tertiary)]">
        {s.status === "cached" ? "cached" : s.status === "running" ? "" : formatStepDuration(s.durationS)}
      </span>
    </>
  );
  return (
    <li className={cn(failed && !compact && "bg-[var(--danger)]/[0.06]")}>
      {expandable ? (
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className={cn(
            "flex w-full items-center gap-2 px-3 text-left hover:bg-[var(--console-bg-hover)]",
            compact ? "py-1" : "py-1.5",
          )}
        >
          {header}
        </button>
      ) : (
        <div className={cn("flex items-center gap-2 px-3", compact ? "py-1" : "py-1.5")}>{header}</div>
      )}
      {open && expandable && (
        <div className="pb-2">
          <ol>
            {(matcher ? s.lines.filter((l) => matcher.test(l.text)) : s.lines).map((l) => {
              const isFocus = l.idx === focusIdx;
              return (
                <li
                  key={l.idx}
                  ref={isFocus && focusRef ? (el) => void (focusRef.current = el) : undefined}
                  className={cn(
                    "flex gap-3 border-l-2 pl-[calc(0.75rem+0.875rem+0.5rem-2px)] pr-3",
                    l.error ? "border-[var(--danger)] bg-[var(--danger)]/10" : "border-transparent",
                  )}
                >
                  <span aria-hidden className="w-11 shrink-0 select-none text-right text-[var(--text-tertiary)] tabular-nums">
                    {l.t !== undefined ? `${l.t.toFixed(1)}s` : ""}
                  </span>
                  <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">
                    <AnsiText text={l.text} highlight={matcher?.re} />
                  </span>
                  {l.repeat > 1 && (
                    <span
                      className="shrink-0 self-start rounded bg-[var(--bg-tertiary)] px-1 font-sans text-2xs text-[var(--text-secondary)]"
                      title={`Printed ${l.repeat} times in a row`}
                    >
                      ×{l.repeat}
                    </span>
                  )}
                </li>
              );
            })}
          </ol>
          {s.errorMessage && (
            <p className="mt-1 border-l-2 border-[var(--danger)] bg-[var(--danger)]/10 py-1 pl-[calc(0.75rem+0.875rem+0.5rem-2px)] pr-3 text-[var(--danger)]">
              {s.errorMessage}
            </p>
          )}
        </div>
      )}
    </li>
  );
}

const STATE_COPY: Record<LiveLogState, { label: string; dot: string }> = {
  connecting: { label: "Connecting…", dot: "bg-[var(--text-tertiary)] motion-safe:animate-pulse" },
  live: { label: "Live", dot: "bg-[var(--success)] motion-safe:animate-pulse" },
  reconnecting: { label: "Connection dropped · reconnecting…", dot: "bg-[var(--warning)] motion-safe:animate-pulse" },
  ended: { label: "Build finished", dot: "bg-[var(--text-tertiary)]" },
  offline: { label: "Can't reach the log stream", dot: "bg-[var(--error)]" },
};

function StreamState({
  state,
  message,
  onReconnect,
  empty,
}: {
  state: LiveLogState;
  message: string | null;
  onReconnect: () => void;
  empty: boolean;
}) {
  // A stream that ended before printing anything (its pod never started)
  // isn't a finished build.
  const closedEarly = state === "ended" && empty;
  const c = closedEarly ? { label: "Log stream closed", dot: STATE_COPY.ended.dot } : STATE_COPY[state];
  return (
    <span className="inline-flex min-w-0 items-center gap-2 text-xs text-[var(--text-secondary)]" role="status">
      <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", c.dot)} />
      <span className="truncate" title={message ?? undefined}>
        {c.label}
      </span>
      {(state === "reconnecting" || state === "offline" || closedEarly) && (
        <button
          type="button"
          onClick={onReconnect}
          className="inline-flex items-center gap-1 rounded border border-[var(--border-subtle)] px-1.5 py-0.5 text-xs text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)]"
        >
          <RefreshCw className="h-3 w-3" />
          Reconnect
        </button>
      )}
    </span>
  );
}
