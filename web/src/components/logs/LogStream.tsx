"use client";

import { memo, useEffect, useMemo, useRef, useState } from "react";
import { useLogStream, type LogLine, type StreamConnection } from "@/features/logs";
import { Button } from "@/components/ui/button";
import { ChevronDown, Copy, RotateCcw, Search, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { AnsiText } from "./AnsiText";

interface Props {
  project: string;
  service: string;
  env?: string;
  height?: string; // e.g. "40vh"
}

export function LogStream({ project, service, env = "production", height = "40vh" }: Props) {
  const { lines, phase, connection, error, notice, clear, reconnect } = useLogStream(project, service, env, 200);
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(false);
  const [filter, setFilter] = useState("");
  // Filter is plain substring by default. A leading "/" turns it into
  // a regex (matches grep -E semantics on the line text). We compile
  // once per filter change rather than per line.
  const matcher = useMemo<((s: string) => boolean) | null>(() => {
    const q = filter.trim();
    if (!q) return null;
    if (q.startsWith("/") && q.length > 1) {
      try {
        const re = new RegExp(q.slice(1), "i");
        return (s) => re.test(s);
      } catch {
        return null; // invalid regex → fall through to "no filter"
      }
    }
    const lower = q.toLowerCase();
    return (s) => s.toLowerCase().includes(lower);
  }, [filter]);
  const visibleLines = useMemo(
    () => (matcher ? lines.filter((l) => matcher(l.line)) : lines),
    [lines, matcher]
  );
  const scrollerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!follow) return;
    const el = scrollerRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [visibleLines, follow]);

  const onScroll = () => {
    const el = scrollerRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
    // Scrolling back down to the tail resumes following, same as the
    // "jump to live" pill.
    if (atBottom !== follow) setFollow(atBottom);
  };

  const conn = CONNECTION_META[connection];
  return (
    // flex column + min-h-0 so a parent that gives us a fixed height
    // (h-72 in BuildLogs) can shrink the scroller properly. Without
    // this the scroller grows past its parent and the user can't
    // scroll because the wheel target is already at the page bottom.
    <div className="flex h-full min-h-0 flex-col rounded-md border border-[var(--border-subtle)] overflow-hidden">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-xs">
        <div className="flex items-center gap-2">
          <span aria-hidden className={cn("h-2 w-2 rounded-full", conn.dot)} />
          <span className="sr-only">Stream status:</span>
          <span className="text-xs font-medium text-[var(--text-primary)]">{conn.label}</span>
          {phase && (
            <span className="text-xs text-[var(--accent)] font-medium">
              · {phase}
            </span>
          )}
          {(connection === "reconnecting" || connection === "failed") && (
            <button
              type="button"
              onClick={reconnect}
              className="text-xs text-[var(--text-primary)] underline underline-offset-2"
            >
              {connection === "failed" ? "Reconnect" : "Retry now"}
            </button>
          )}
          {connection === "failed" && error && (
            <span className="text-xs text-[var(--error)]">· {error}</span>
          )}
        </div>
        <div className="flex items-center gap-1">
          <label className="flex items-center gap-1.5 text-xs text-[var(--text-secondary)]">
            <input
              type="checkbox"
              checked={follow}
              onChange={(e) => setFollow(e.target.checked)}
              className="h-3 w-3"
            />
            follow
          </label>
          <label className="flex items-center gap-1.5 text-xs text-[var(--text-secondary)]">
            <input
              type="checkbox"
              checked={wrap}
              onChange={(e) => setWrap(e.target.checked)}
              className="h-3 w-3"
            />
            wrap
          </label>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label="Copy all"
            onClick={async () => {
              // Clean copy: strip the per-line pod prefix entirely so
              // pasting into a chat or issue tracker doesn't drag along
              // 12-char pod hashes on every line. User just sees the
              // app's own log lines, joined with newlines.
              const text = lines.map((l) => l.line).join("\n");
              try {
                await navigator.clipboard.writeText(text);
                toast.success(`Copied ${lines.length} lines`);
              } catch {
                toast.error("Copy failed (clipboard API unavailable)");
              }
            }}
          >
            <Copy className="h-3 w-3" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label="Clear"
            onClick={clear}
          >
            <X className="h-3 w-3" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label="Jump to bottom"
            onClick={() => setFollow(true)}
          >
            <ChevronDown className="h-3 w-3" />
          </Button>
        </div>
      </div>
      {/* Filter row. Substring by default; leading "/" enables regex.
          Stays in the chrome (out of the scroll viewport) so the
          input doesn't disappear when logs scroll. Empty filter is a
          no-op — visibleLines === lines. */}
      <div className="relative shrink-0 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5">
        <Search className="pointer-events-none absolute left-4 top-1/2 h-3 w-3 -translate-y-1/2 text-[var(--text-tertiary)]" />
        <input
          type="text"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="filter — type a substring, or /regex"
          className="h-6 w-full rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] py-0.5 pl-6 pr-16 font-mono text-xs text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:border-[var(--border-strong)] focus:outline-none"
          spellCheck={false}
        />
        <span className="pointer-events-none absolute right-4 top-1/2 -translate-y-1/2 font-mono text-xs text-[var(--text-tertiary)]">
          {filter
            ? `${visibleLines.length}/${lines.length}`
            : `${lines.length}`}
        </span>
      </div>
      <div className="relative min-h-0 flex-1">
        {/* Scroll-pause indicator. When `follow` is off (the user
            scrolled up) we render a floating "Jump to live" pill at
            the bottom of the viewport. The same button restores
            follow=true and snaps to the tail. Without this, the
            stream silently kept appending while the user thought
            they were caught up — they'd think the stream stalled
            and reload, losing buffered context. */}
        {!follow && lines.length > 0 && (
          <button
            type="button"
            onClick={() => {
              setFollow(true);
              const el = scrollerRef.current;
              if (el) el.scrollTop = el.scrollHeight;
            }}
            className="absolute bottom-3 left-1/2 z-10 -translate-x-1/2 rounded-full border border-amber-500/40 bg-[var(--console-bg)]/95 px-3 py-1 text-xs text-amber-200 shadow-md backdrop-blur hover:bg-[var(--console-bg-hover)] font-medium"
            aria-label="scroll paused — jump to live tail"
          >
            paused · jump to live ↓
          </button>
        )}
      <div
        ref={scrollerRef}
        onScroll={onScroll}
        // When the parent is height-constrained (BuildLogs in the
        // deployments panel passes h-72), height="100%" lets us fill
        // it and scroll within. When the parent is unbounded (an
        // ad-hoc embed with no flex parent), maxHeight caps us at the
        // requested viewport unit so we don't push the page.
        style={height === "100%" ? undefined : { maxHeight: height }}
        className="dark h-full min-h-0 overflow-auto bg-[var(--console-bg)] p-3 font-mono text-xs leading-relaxed text-zinc-200"
      >
        {lines.length === 0 && (
          <p className="text-[var(--text-tertiary)]">
            {notice
              ? notice
              : connection === "live"
                ? "waiting for logs…"
                : connection === "ended"
                  ? "stream ended"
                  : connection === "reconnecting"
                    ? "reconnecting…"
                    : connection === "failed"
                      ? "couldn't connect to the log stream"
                      : "connecting…"}
          </p>
        )}
        {lines.length > 0 && visibleLines.length === 0 && filter && (
          <p className="text-[var(--text-tertiary)]">
            no lines match{" "}
            <span className="font-mono text-[var(--text-secondary)]">{filter}</span>
          </p>
        )}
        {visibleLines.map((l) => (
          <LogRow key={l.id} line={l} wrap={wrap} />
        ))}
      </div>
      </div>
    </div>
  );
}

const CONNECTION_META: Record<StreamConnection, { dot: string; label: string }> = {
  connecting: { dot: "bg-[var(--text-tertiary)] animate-pulse motion-reduce:animate-none", label: "Connecting…" },
  live: { dot: "bg-[var(--success)]", label: "Live" },
  reconnecting: { dot: "bg-[var(--building)] animate-pulse motion-reduce:animate-none", label: "Reconnecting…" },
  ended: { dot: "bg-[var(--text-tertiary)]", label: "Ended" },
  failed: { dot: "bg-[var(--error)]", label: "Disconnected" },
};

// Memoized per row: a new batch of lines re-renders only the rows it
// added, not all 10k. content-visibility lets the browser skip layout
// and paint for rows scrolled out of view.
const LogRow = memo(function LogRow({ line: l, wrap }: { line: LogLine; wrap: boolean }) {
  const podShort = l.pod.length > 12 ? l.pod.slice(-12) : l.pod;
  return (
    <div
      className={cn(
        "py-px [content-visibility:auto] [contain-intrinsic-size:auto_1.4em]",
        wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre",
        l.stream === "stderr" && "text-red-400"
      )}
    >
      {/* Pod prefix as a CSS ::before-style decoration: the
          user sees it, but the value isn't a child text node
          of the line wrapper, so a drag-select copy + paste
          carries only `l.line`. select-none is a belt for
          browsers that still drag the inline span on copy. */}
      <span
        aria-hidden="true"
        className="select-none text-zinc-500 mr-2 inline-block"
        style={{ userSelect: "none", WebkitUserSelect: "none" }}
      >
        {podShort}
      </span>
      <span className="select-text">
        <AnsiText text={l.line} />
      </span>
    </div>
  );
});

export function PhaseStepper({ phase }: { phase?: string | null }) {
  const steps = ["CLONING", "INSTALLING", "BUILDING", "PUSHING", "DEPLOYING", "ACTIVE"];
  const currentIdx = steps.indexOf((phase ?? "").toUpperCase());
  return (
    <div className="flex items-center gap-1 text-xs font-medium">
      {steps.map((s, i) => {
        const done = i < currentIdx;
        const active = i === currentIdx;
        return (
          <span
            key={s}
            className={cn(
              "rounded border px-1.5 py-0.5",
              done && "border-[var(--success)]/30 bg-[var(--success-subtle)] text-[var(--success)]",
              active && "border-[var(--accent)]/30 bg-[var(--accent-subtle)] text-[var(--accent)] animate-pulse",
              !done && !active && "border-[var(--border-subtle)] text-[var(--text-tertiary)]"
            )}
          >
            {s.charAt(0) + s.slice(1).toLowerCase()}
            {i < steps.length - 1 && <span className="mx-1 opacity-50">→</span>}
          </span>
        );
      })}
      {/* Reset icon to show that this is the latest phase indicator */}
      <RotateCcw className="ml-2 h-3 w-3 text-[var(--text-tertiary)]" />
    </div>
  );
}
