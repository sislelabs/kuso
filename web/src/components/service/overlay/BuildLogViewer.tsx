"use client";

import { memo, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, Download, RefreshCw, Search, WrapText } from "lucide-react";
import {
  extractFailureLines,
  useLiveBuildLog,
  useStoredBuildLog,
  type LiveLogState,
} from "@/features/builds";
import { ApiError } from "@/lib/api-client";
import { cn } from "@/lib/utils";
import { CopyButton } from "@/components/ui/copy-button";

const ERRORISH = /\b(error|fatal|panic|failed|exit code)\b|>>>/i;

type Mode = "errors" | "full";

// BuildLogViewer shows one build's log. Running builds stream over the
// WebSocket (with reconnect); finished builds read the stored log once. A
// failed build opens on the failure summary, the full log is one click away.
export const BuildLogViewer = memo(function BuildLogViewer({
  project,
  service,
  buildId,
  live,
  failed,
}: {
  project: string;
  service: string;
  buildId: string;
  live: boolean;
  failed: boolean;
}) {
  const stored = useStoredBuildLog(project, service, buildId, !live);
  const stream = useLiveBuildLog(project, service, buildId, live);

  const lines = useMemo(
    () => (live ? stream.lines.map((l) => l.line) : (stored.data ?? []).map((l) => l.line)),
    [live, stream.lines, stored.data],
  );
  const failureLines = useMemo(() => (failed ? extractFailureLines(lines) : []), [failed, lines]);
  const [mode, setMode] = useState<Mode>("errors");
  const effectiveMode: Mode = failed && failureLines.length > 0 ? mode : "full";
  const [filter, setFilter] = useState("");
  const [wrap, setWrap] = useState(true);

  const shown = useMemo(() => {
    const base = effectiveMode === "errors" ? failureLines : lines;
    const q = filter.trim().toLowerCase();
    return q ? base.filter((l) => l.toLowerCase().includes(q)) : base;
  }, [effectiveMode, failureLines, lines, filter]);

  const scroller = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  useEffect(() => {
    if (!live || !follow) return;
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [shown, live, follow]);

  const loading = live ? stream.state === "connecting" && lines.length === 0 : stored.isPending;
  const loadError =
    !live && stored.isError
      ? stored.error instanceof ApiError && stored.error.status === 404
        ? stored.error.message || "This build's log is no longer available."
        : "Couldn't load the build log."
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

  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5">
        {failed && failureLines.length > 0 && (
          <div role="group" aria-label="Log view" className="inline-flex rounded-md border border-[var(--border-subtle)] p-0.5">
            {(["errors", "full"] as const).map((m) => (
              <button
                key={m}
                type="button"
                aria-pressed={effectiveMode === m}
                onClick={() => setMode(m)}
                className={cn(
                  "rounded px-2 py-0.5 text-xs transition-colors",
                  effectiveMode === m
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                )}
              >
                {m === "errors" ? `Failure (${failureLines.length})` : `Full log (${lines.length})`}
              </button>
            ))}
          </div>
        )}
        {live ? (
          <StreamState state={stream.state} message={stream.message} onReconnect={stream.reconnect} />
        ) : (
          !(failed && failureLines.length > 0) &&
          stored.data && <span className="text-xs text-[var(--text-secondary)]">{lines.length} lines</span>
        )}
        <div className="ml-auto flex items-center gap-1">
          <label className="relative">
            <span className="sr-only">Filter log lines</span>
            <Search className="pointer-events-none absolute left-1.5 top-1/2 h-3 w-3 -translate-y-1/2 text-[var(--text-tertiary)]" />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter"
              spellCheck={false}
              className="h-6 w-28 rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] pl-5 pr-1.5 text-xs text-[var(--text-primary)] outline-none placeholder:text-[var(--text-tertiary)] focus:border-[var(--border-strong)] sm:w-36"
            />
          </label>
          <button
            type="button"
            aria-pressed={wrap}
            onClick={() => setWrap((w) => !w)}
            title={wrap ? "Don't wrap lines" : "Wrap lines"}
            aria-label="Wrap lines"
            className={cn(
              "inline-flex h-6 w-6 items-center justify-center rounded hover:bg-[var(--bg-tertiary)]",
              wrap ? "text-[var(--text-primary)]" : "text-[var(--text-tertiary)]",
            )}
          >
            <WrapText className="h-3.5 w-3.5" />
          </button>
          <CopyButton value={shown.join("\n")} label="Copy log" />
          <button
            type="button"
            onClick={download}
            disabled={lines.length === 0}
            title="Download .log"
            aria-label="Download log"
            className="inline-flex h-6 w-6 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-40"
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
          className="max-h-[420px] min-h-[96px] overflow-auto bg-[var(--console-bg)] py-2 font-mono text-[12px] leading-[1.55] text-[var(--console-fg)]"
          role="log"
          aria-live={live ? "polite" : undefined}
          aria-label="Build log"
        >
          {loading ? (
            <p className="px-3 text-[var(--text-tertiary)]">Loading log…</p>
          ) : loadError ? (
            <p className="px-3 text-[var(--text-secondary)]">{loadError}</p>
          ) : shown.length === 0 ? (
            <p className="px-3 text-[var(--text-tertiary)]">
              {filter ? "No lines match the filter." : live ? "Waiting for the build to print…" : "The build printed nothing."}
            </p>
          ) : (
            <ol>
              {shown.map((l, i) => (
                <li
                  key={i}
                  className={cn(
                    "flex gap-3 px-3",
                    effectiveMode === "full" && failed && ERRORISH.test(l) && "bg-[var(--error)]/10",
                  )}
                >
                  <span aria-hidden className="w-8 shrink-0 select-none text-right text-[var(--text-tertiary)]/70">
                    {i + 1}
                  </span>
                  <span className={cn("min-w-0", wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre")}>{l}</span>
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

const STATE_COPY: Record<LiveLogState, { label: string; dot: string }> = {
  connecting: { label: "Connecting…", dot: "bg-[var(--text-tertiary)] animate-pulse" },
  live: { label: "Live", dot: "bg-[var(--success)] animate-pulse" },
  reconnecting: { label: "Connection dropped · reconnecting…", dot: "bg-[var(--warning)] animate-pulse" },
  ended: { label: "Build finished", dot: "bg-[var(--text-tertiary)]" },
  offline: { label: "Can't reach the log stream", dot: "bg-[var(--error)]" },
};

function StreamState({
  state,
  message,
  onReconnect,
}: {
  state: LiveLogState;
  message: string | null;
  onReconnect: () => void;
}) {
  const c = STATE_COPY[state];
  return (
    <span className="inline-flex min-w-0 items-center gap-2 text-xs text-[var(--text-secondary)]" role="status">
      <span aria-hidden className={cn("h-2 w-2 shrink-0 rounded-full", c.dot)} />
      <span className="truncate" title={message ?? undefined}>
        {c.label}
      </span>
      {(state === "reconnecting" || state === "offline") && (
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
