"use client";

import { useEffect, useRef, useState } from "react";
import { useLogStream } from "@/features/logs";
import { Button } from "@/components/ui/button";
import { ChevronDown, RotateCcw, X } from "lucide-react";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
  env?: string;
  height?: string; // e.g. "40vh"
}

export function LogStream({ project, service, env = "production", height = "40vh" }: Props) {
  const { lines, phase, status, error, clear } = useLogStream(project, service, env, 200);
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(false);
  const scrollerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!follow) return;
    const el = scrollerRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines, follow]);

  const onScroll = () => {
    const el = scrollerRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
    if (!atBottom && follow) setFollow(false);
  };

  const statusColor =
    status === "open"
      ? "bg-emerald-500"
      : status === "connecting"
        ? "bg-amber-500 animate-pulse"
        : status === "error"
          ? "bg-red-500"
          : "bg-[var(--text-tertiary)]";

  return (
    // flex column + min-h-0 so a parent that gives us a fixed height
    // (h-72 in BuildLogs) can shrink the scroller properly. Without
    // this the scroller grows past its parent and the user can't
    // scroll because the wheel target is already at the page bottom.
    <div className="flex h-full min-h-0 flex-col rounded-md border border-[var(--border-subtle)] overflow-hidden">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-2 text-xs">
        <div className="flex items-center gap-2">
          <span className={cn("h-2 w-2 rounded-full", statusColor)} />
          <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            {status}
          </span>
          {phase && (
            <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--accent)]">
              · {phase}
            </span>
          )}
          {error && (
            <span className="font-mono text-[10px] text-red-500">· {error}</span>
          )}
        </div>
        <div className="flex items-center gap-1">
          <label className="flex items-center gap-1.5 text-[10px] text-[var(--text-secondary)]">
            <input
              type="checkbox"
              checked={follow}
              onChange={(e) => setFollow(e.target.checked)}
              className="h-3 w-3"
            />
            follow
          </label>
          <label className="flex items-center gap-1.5 text-[10px] text-[var(--text-secondary)]">
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
      <div
        ref={scrollerRef}
        onScroll={onScroll}
        // When the parent is height-constrained (BuildLogs in the
        // deployments panel passes h-72), height="100%" lets us fill
        // it and scroll within. When the parent is unbounded (an
        // ad-hoc embed with no flex parent), maxHeight caps us at the
        // requested viewport unit so we don't push the page.
        style={height === "100%" ? undefined : { maxHeight: height }}
        className="min-h-0 flex-1 overflow-auto bg-[#0b0b0e] p-3 font-mono text-[11px] leading-relaxed text-zinc-200"
      >
        {lines.length === 0 && (
          <p className="text-[var(--text-tertiary)]">
            {status === "connecting"
              ? "connecting…"
              : status === "open"
                ? "waiting for logs…"
                : "disconnected"}
          </p>
        )}
        {lines.map((l, i) => (
          <div
            key={i}
            className={cn(
              "py-px",
              wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre",
              l.stream === "stderr" && "text-red-400"
            )}
          >
            <span className="select-none text-zinc-500 mr-2">
              {l.pod.length > 12 ? l.pod.slice(-12) : l.pod}
            </span>
            {l.line}
          </div>
        ))}
      </div>
    </div>
  );
}

export function PhaseStepper({ phase }: { phase?: string | null }) {
  const steps = ["CLONING", "INSTALLING", "BUILDING", "PUSHING", "DEPLOYING", "ACTIVE"];
  const currentIdx = steps.indexOf((phase ?? "").toUpperCase());
  return (
    <div className="flex items-center gap-1 text-[10px] font-mono uppercase tracking-widest">
      {steps.map((s, i) => {
        const done = i < currentIdx;
        const active = i === currentIdx;
        return (
          <span
            key={s}
            className={cn(
              "rounded border px-1.5 py-0.5",
              done && "border-emerald-500/30 bg-emerald-500/10 text-emerald-500",
              active && "border-[var(--accent)]/30 bg-[var(--accent-subtle)] text-[var(--accent)] animate-pulse",
              !done && !active && "border-[var(--border-subtle)] text-[var(--text-tertiary)]"
            )}
          >
            {s}
            {i < steps.length - 1 && <span className="mx-1 opacity-50">→</span>}
          </span>
        );
      })}
      {/* Reset icon to show that this is the latest phase indicator */}
      <RotateCcw className="ml-2 h-3 w-3 text-[var(--text-tertiary)]" />
    </div>
  );
}
